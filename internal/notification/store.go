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
)

// Record is a persisted notification request, ready for delivery.
type Record struct {
	ID          string
	VendorID    string
	RenderedBody string
	Headers     map[string]string
	TargetURL   string
	Method      string
	Status      Status
	CreatedAt   time.Time
}

// Store persists notification records before delivery.
type Store interface {
	Save(ctx context.Context, r *Record) error
}

// PersistingEnqueuer implements Enqueuer and durably persists each notification
// before returning an ID. WOR-13 will attach an async delivery worker that reads
// from the same store.
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
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS notifications (
			id            TEXT PRIMARY KEY,
			vendor_id     TEXT NOT NULL,
			rendered_body TEXT NOT NULL,
			headers       TEXT NOT NULL,
			target_url    TEXT NOT NULL,
			method        TEXT NOT NULL,
			status        TEXT NOT NULL DEFAULT 'pending',
			created_at    TIMESTAMP NOT NULL
		)
	`)
	if err != nil {
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Save(ctx context.Context, r *Record) error {
	headers, err := json.Marshal(r.Headers)
	if err != nil {
		return fmt.Errorf("marshalling headers: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO notifications (id, vendor_id, rendered_body, headers, target_url, method, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.VendorID, r.RenderedBody, string(headers), r.TargetURL, r.Method, string(r.Status), r.CreatedAt,
	)
	return err
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

// InMemoryStore persists notifications in memory — used in tests.
type InMemoryStore struct {
	Records []*Record
}

func (m *InMemoryStore) Save(_ context.Context, r *Record) error {
	m.Records = append(m.Records, r)
	return nil
}
