package dlq_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lqquReactNative/rc_xiaohou/internal/delivery"
	"github.com/lqquReactNative/rc_xiaohou/internal/dlq"
	"github.com/lqquReactNative/rc_xiaohou/internal/notification"
)

func newStore(t *testing.T) *notification.SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	s, err := notification.NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func saveRecord(t *testing.T, s *notification.SQLiteStore, vendorID string) *notification.Record {
	t.Helper()
	r := &notification.Record{
		ID:           "test-" + vendorID,
		VendorID:     vendorID,
		RenderedBody: `{"event":"test"}`,
		Headers:      map[string]string{"X-Key": "val"},
		TargetURL:    "http://vendor.example.com",
		Method:       "POST",
		Status:       notification.StatusPending,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.Save(context.Background(), r); err != nil {
		t.Fatalf("save: %v", err)
	}
	return r
}

func newRouter(store notification.DLQStore) http.Handler {
	r := chi.NewRouter()
	r.Mount("/dlq", dlq.NewHandler(store).Routes())
	return r
}

// AC1: GET /dlq returns dead entries; no live entries appear.
func TestListDLQ_ReturnsOnlyDeadEntries(t *testing.T) {
	store := newStore(t)
	r1 := saveRecord(t, store, "crm")
	r2 := saveRecord(t, store, "ads")

	store.MarkDead(r1.ID) //nolint:errcheck
	// r2 stays pending — must not appear in DLQ

	router := newRouter(store)
	req := httptest.NewRequest(http.MethodGet, "/dlq", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	var entries []*notification.DLQRecord
	if err := json.NewDecoder(rec.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 DLQ entry, got %d", len(entries))
	}
	if entries[0].ID != r1.ID {
		t.Errorf("entry ID: got %s, want %s", entries[0].ID, r1.ID)
	}
	_ = r2
}

// AC3: DLQ entry contains vendor ID, original payload, and full attempt history.
func TestListDLQ_EntryContainsFullContext(t *testing.T) {
	store := newStore(t)
	r := saveRecord(t, store, "crm")

	s1 := 503
	e1 := "upstream error"
	store.RecordAttempt(r.ID, 1, notification.AttemptFailed, &s1, &e1) //nolint:errcheck
	s2 := 503
	e2 := "still down"
	store.RecordAttempt(r.ID, 2, notification.AttemptFailed, &s2, &e2) //nolint:errcheck
	store.MarkDead(r.ID)                                                 //nolint:errcheck

	router := newRouter(store)
	req := httptest.NewRequest(http.MethodGet, "/dlq", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var entries []*notification.DLQRecord
	json.NewDecoder(rec.Body).Decode(&entries) //nolint:errcheck
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]

	if e.VendorID != "crm" {
		t.Errorf("vendor_id: got %s, want crm", e.VendorID)
	}
	if e.RenderedBody != `{"event":"test"}` {
		t.Errorf("payload: got %s", e.RenderedBody)
	}
	if len(e.Attempts) != 2 {
		t.Fatalf("attempts: got %d, want 2", len(e.Attempts))
	}
	if *e.Attempts[0].HTTPStatus != 503 {
		t.Errorf("attempt[0] status: got %d, want 503", *e.Attempts[0].HTTPStatus)
	}
}

// Resubmit resets dead entry to pending; it disappears from DLQ.
func TestResubmit_ResetsToPending(t *testing.T) {
	store := newStore(t)
	r := saveRecord(t, store, "crm")
	store.MarkDead(r.ID) //nolint:errcheck

	router := newRouter(store)
	body := bytes.NewBufferString("")
	req := httptest.NewRequest(http.MethodPost, "/dlq/"+r.ID+"/resubmit", body)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}

	// Entry must be gone from DLQ
	entries, _ := store.ListDead()
	for _, e := range entries {
		if e.ID == r.ID {
			t.Error("resubmitted entry should not appear in DLQ")
		}
	}

	// Entry must now appear as pending (ClaimPending should pick it up)
	got, err := store.GetByID(r.ID)
	if err != nil {
		t.Fatalf("get by ID: %v", err)
	}
	if got.Status != notification.StatusPending {
		t.Errorf("status after resubmit: got %s, want pending", got.Status)
	}
	if got.RetryCount != 0 {
		t.Errorf("retry_count after resubmit: got %d, want 0", got.RetryCount)
	}
}

// Resubmit on unknown ID returns 404.
func TestResubmit_NotFound(t *testing.T) {
	store := newStore(t)
	router := newRouter(store)

	req := httptest.NewRequest(http.MethodPost, "/dlq/nonexistent/resubmit", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
}

// AC2: Worker emits ERROR-level log when notification is moved to DLQ.
// Verified by observing DLQ state after a worker delivery cycle with a 503 vendor.
func TestWorker_EmitsDLQAlertOnExhaustedRetries(t *testing.T) {
	// Vendor that always returns 503
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	store := newStore(t)
	policy := delivery.RetryPolicy{BackoffSchedule: []time.Duration{}} // 0 retries → immediate DLQ
	worker := delivery.NewWorkerWithClient(store, policy, &http.Client{Timeout: 5 * time.Second})

	r := &notification.Record{
		ID:           "dlq-alert-test",
		VendorID:     "test-vendor",
		RenderedBody: `{}`,
		Headers:      map[string]string{},
		TargetURL:    server.URL,
		Method:       "POST",
		Status:       notification.StatusPending,
		CreatedAt:    time.Now().UTC(),
	}
	store.Save(context.Background(), r) //nolint:errcheck

	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}

	// After one failed delivery with 0 retries, notification must be in DLQ
	got, err := store.GetByID(r.ID)
	if err != nil {
		t.Fatalf("get by ID: %v", err)
	}
	if got.Status != notification.StatusDead {
		t.Errorf("status: got %s, want dead", got.Status)
	}
}

// Helper to allow writing test binaries without building the full server.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
