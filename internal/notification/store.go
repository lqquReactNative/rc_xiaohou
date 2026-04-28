package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusDelivered Status = "delivered"
	StatusFailed    Status = "failed"
	StatusDead      Status = "dead" // retry budget exhausted
)

type AttemptStatus string

const (
	AttemptSuccess AttemptStatus = "success"
	AttemptFailed  AttemptStatus = "failed"
)

// Record is a persisted notification request, ready for delivery.
type Record struct {
	ID           string
	VendorID     string
	RenderedBody string
	Headers      map[string]string
	TargetURL    string
	Method       string
	Status       Status
	RetryCount   int
	NextRetryAt  *time.Time
	CreatedAt    time.Time
}

// DeliveryAttempt records a single outbound delivery attempt.
type DeliveryAttempt struct {
	ID             string        `json:"id"`
	NotificationID string        `json:"notification_id"`
	AttemptNumber  int           `json:"attempt_number"`
	Status         AttemptStatus `json:"status"`
	HTTPStatus     *int          `json:"http_status,omitempty"`
	Error          *string       `json:"error,omitempty"`
	LatencyMs      int64         `json:"latency_ms"` // round-trip time to vendor (AC3)
	CreatedAt      time.Time     `json:"created_at"`
}

// Store persists notification records before delivery.
type Store interface {
	Save(ctx context.Context, r *Record) error
}

// DeliveryStore is the extended interface used by the delivery worker.
// SQLiteStore implements both Store and DeliveryStore.
type DeliveryStore interface {
	// ClaimPending atomically fetches notifications ready for delivery and
	// marks them as 'delivering' to prevent double-processing.
	ClaimPending(limit int) ([]*Record, error)
	MarkDelivered(id string) error
	// ScheduleRetry sets the notification back to pending with a future retry time.
	ScheduleRetry(id string, retryCount int, nextRetryAt time.Time) error
	// MarkFailed permanently fails a notification (4xx or non-retriable error).
	MarkFailed(id string) error
	// MarkDead moves the notification to dead-letter state (retry budget exhausted).
	MarkDead(id string) error
	RecordAttempt(notifID string, attemptNumber int, status AttemptStatus, httpStatus *int, errMsg *string, latencyMs int64) error
	// ResetNextRetryAt clears next_retry_at for test helpers.
	ResetNextRetryAt(id string) error
}

// PersistingEnqueuer implements Enqueuer and durably persists each notification
// before returning an ID. The delivery worker reads from the same SQLiteStore.
type PersistingEnqueuer struct {
	store Store
}

func NewPersistingEnqueuer(store Store) *PersistingEnqueuer {
	return &PersistingEnqueuer{store: store}
}

func (e *PersistingEnqueuer) Enqueue(vendorID, renderedBody string, headers map[string]string, targetURL, method string) (string, error) {
	r := &Record{
		ID:           uuid.New().String(),
		VendorID:     vendorID,
		RenderedBody: renderedBody,
		Headers:      headers,
		TargetURL:    targetURL,
		Method:       method,
		Status:       StatusPending,
		CreatedAt:    time.Now().UTC(),
	}
	if err := e.store.Save(context.Background(), r); err != nil {
		return "", fmt.Errorf("persisting notification: %w", err)
	}
	return r.ID, nil
}

// SQLiteStore persists notification records to a SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(dsn string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA foreign_keys=ON;

		CREATE TABLE IF NOT EXISTS notifications (
			id            TEXT PRIMARY KEY,
			vendor_id     TEXT NOT NULL,
			rendered_body TEXT NOT NULL,
			headers       TEXT NOT NULL,
			target_url    TEXT NOT NULL,
			method        TEXT NOT NULL,
			status        TEXT NOT NULL DEFAULT 'pending',
			retry_count   INTEGER NOT NULL DEFAULT 0,
			next_retry_at TIMESTAMP,
			created_at    TIMESTAMP NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_notifications_pending
			ON notifications(status, next_retry_at);

		CREATE TABLE IF NOT EXISTS delivery_attempts (
			id              TEXT PRIMARY KEY,
			notification_id TEXT NOT NULL REFERENCES notifications(id),
			attempt_number  INTEGER NOT NULL,
			status          TEXT NOT NULL,
			http_status     INTEGER,
			error           TEXT,
			latency_ms      INTEGER NOT NULL DEFAULT 0,
			created_at      TIMESTAMP NOT NULL
		);
	`)
	if err != nil {
		return err
	}
	// Migration: add latency_ms to existing delivery_attempts tables (idempotent).
	_, _ = db.Exec(`ALTER TABLE delivery_attempts ADD COLUMN latency_ms INTEGER NOT NULL DEFAULT 0`)
	return nil
}

func (s *SQLiteStore) Save(ctx context.Context, r *Record) error {
	headers, err := json.Marshal(r.Headers)
	if err != nil {
		return fmt.Errorf("marshalling headers: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO notifications (id, vendor_id, rendered_body, headers, target_url, method, status, retry_count, next_retry_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.VendorID, r.RenderedBody, string(headers), r.TargetURL, r.Method,
		string(r.Status), 0, nil, r.CreatedAt,
	)
	return err
}

func (s *SQLiteStore) ClaimPending(limit int) ([]*Record, error) {
	now := time.Now().UTC()
	rows, err := s.db.Query(`
		SELECT id, vendor_id, rendered_body, headers, target_url, method, status, retry_count, next_retry_at, created_at
		FROM notifications
		WHERE status = 'pending'
		  AND (next_retry_at IS NULL OR next_retry_at <= ?)
		ORDER BY created_at
		LIMIT ?
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, r := range records {
		_, err := s.db.Exec(`UPDATE notifications SET status='delivering' WHERE id=?`, r.ID)
		if err != nil {
			return nil, err
		}
	}
	return records, nil
}

func (s *SQLiteStore) MarkDelivered(id string) error {
	_, err := s.db.Exec(`UPDATE notifications SET status='delivered' WHERE id=?`, id)
	return err
}

func (s *SQLiteStore) ScheduleRetry(id string, retryCount int, nextRetryAt time.Time) error {
	_, err := s.db.Exec(
		`UPDATE notifications SET status='pending', retry_count=?, next_retry_at=? WHERE id=?`,
		retryCount, nextRetryAt.UTC(), id,
	)
	return err
}

func (s *SQLiteStore) MarkFailed(id string) error {
	_, err := s.db.Exec(`UPDATE notifications SET status='failed' WHERE id=?`, id)
	return err
}

func (s *SQLiteStore) MarkDead(id string) error {
	_, err := s.db.Exec(`UPDATE notifications SET status='dead' WHERE id=?`, id)
	return err
}

func (s *SQLiteStore) RecordAttempt(notifID string, attemptNumber int, status AttemptStatus, httpStatus *int, errMsg *string, latencyMs int64) error {
	_, err := s.db.Exec(
		`INSERT INTO delivery_attempts(id, notification_id, attempt_number, status, http_status, error, latency_ms, created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		uuid.New().String(), notifID, attemptNumber, string(status),
		httpStatus, errMsg, latencyMs, time.Now().UTC(),
	)
	return err
}

// GetByID fetches a single notification by its ID (used in tests).
func (s *SQLiteStore) GetByID(id string) (*Record, error) {
	row := s.db.QueryRow(`
		SELECT id, vendor_id, rendered_body, headers, target_url, method, status, retry_count, next_retry_at, created_at
		FROM notifications WHERE id=?`, id)
	var r Record
	var headersJSON string
	var nextRetryAt *time.Time
	if err := row.Scan(
		&r.ID, &r.VendorID, &r.RenderedBody, &headersJSON,
		&r.TargetURL, &r.Method, &r.Status, &r.RetryCount, &nextRetryAt, &r.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(headersJSON), &r.Headers); err != nil {
		return nil, err
	}
	r.NextRetryAt = nextRetryAt
	return &r, nil
}

// ResetNextRetryAt is used in tests to make a notification immediately eligible.
func (s *SQLiteStore) ResetNextRetryAt(id string) error {
	_, err := s.db.Exec(`UPDATE notifications SET next_retry_at=NULL, status='pending' WHERE id=?`, id)
	return err
}

// GetAttempts returns all delivery attempts for a notification, ordered by creation time.
// Used in tests to verify AC3 (attempt logging with outcome details).
func (s *SQLiteStore) GetAttempts(notifID string) ([]*DeliveryAttempt, error) {
	rows, err := s.db.Query(
		`SELECT id, notification_id, attempt_number, status, http_status, error, latency_ms, created_at
		 FROM delivery_attempts WHERE notification_id=? ORDER BY created_at`, notifID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DeliveryAttempt
	for rows.Next() {
		a := &DeliveryAttempt{}
		if err := rows.Scan(&a.ID, &a.NotificationID, &a.AttemptNumber, &a.Status,
			&a.HTTPStatus, &a.Error, &a.LatencyMs, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(s scanner) (*Record, error) {
	var r Record
	var headersJSON string
	var nextRetryAt *time.Time
	if err := s.Scan(
		&r.ID, &r.VendorID, &r.RenderedBody, &headersJSON,
		&r.TargetURL, &r.Method, &r.Status, &r.RetryCount, &nextRetryAt, &r.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(headersJSON), &r.Headers); err != nil {
		return nil, err
	}
	r.NextRetryAt = nextRetryAt
	return &r, nil
}

// DLQRecord is a dead notification bundled with its full delivery attempt history.
type DLQRecord struct {
	ID           string            `json:"id"`
	VendorID     string            `json:"vendor_id"`
	RenderedBody string            `json:"payload"`
	Headers      map[string]string `json:"headers"`
	TargetURL    string            `json:"target_url"`
	Method       string            `json:"method"`
	Status       Status            `json:"status"`
	RetryCount   int               `json:"retry_count"`
	CreatedAt    time.Time         `json:"created_at"`
	Attempts     []*DeliveryAttempt `json:"attempts"`
}

// DLQStore exposes dead-letter queue inspection and manual resubmission.
// SQLiteStore implements this interface.
type DLQStore interface {
	// ListDead returns all dead notifications together with their attempt history.
	ListDead() ([]*DLQRecord, error)
	// ResubmitDead resets a dead notification to pending for manual re-delivery.
	// Returns (true, nil) when found and reset; (false, nil) when no matching entry.
	ResubmitDead(id string) (bool, error)
}

func (s *SQLiteStore) ListDead() ([]*DLQRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, vendor_id, rendered_body, headers, target_url, method, status, retry_count, next_retry_at, created_at
		FROM notifications WHERE status='dead' ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*DLQRecord
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		attempts, err := s.listAttempts(r.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, &DLQRecord{
			ID:           r.ID,
			VendorID:     r.VendorID,
			RenderedBody: r.RenderedBody,
			Headers:      r.Headers,
			TargetURL:    r.TargetURL,
			Method:       r.Method,
			Status:       r.Status,
			RetryCount:   r.RetryCount,
			CreatedAt:    r.CreatedAt,
			Attempts:     attempts,
		})
	}
	return out, rows.Err()
}

func (s *SQLiteStore) listAttempts(notifID string) ([]*DeliveryAttempt, error) {
	rows, err := s.db.Query(`
		SELECT id, notification_id, attempt_number, status, http_status, error, created_at
		FROM delivery_attempts WHERE notification_id=? ORDER BY attempt_number
	`, notifID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*DeliveryAttempt
	for rows.Next() {
		var a DeliveryAttempt
		if err := rows.Scan(&a.ID, &a.NotificationID, &a.AttemptNumber, &a.Status, &a.HTTPStatus, &a.Error, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ResubmitDead(id string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE notifications SET status='pending', retry_count=0, next_retry_at=NULL WHERE id=? AND status='dead'`,
		id,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// InMemoryStore persists notifications in memory — used in tests.
type InMemoryStore struct {
	Records []*Record
}

func (m *InMemoryStore) Save(_ context.Context, r *Record) error {
	m.Records = append(m.Records, r)
	return nil
}
