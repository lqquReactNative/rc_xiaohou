package delivery_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lqquReactNative/rc_xiaohou/internal/delivery"
	"github.com/lqquReactNative/rc_xiaohou/internal/notification"
)

// WOR-13 AC1: the 202 is only returned after durable write; calling code can rely on this.
func TestAC1_NotificationPersistedBeforeHandlerReturns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "notif_test.db")
	s, err := notification.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	enqueuer := notification.NewPersistingEnqueuer(s)
	id, err := enqueuer.Enqueue("vendor-1", `{"event":"signup"}`,
		map[string]string{"X-Token": "tok"}, "http://example.com/hook", "POST")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Immediately verify the record is in the DB — no async step needed.
	rec, err := s.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID after enqueue: %v", err)
	}
	if rec.Status != notification.StatusPending {
		t.Errorf("expected pending, got %s", rec.Status)
	}
}

// WOR-13 AC2: notifications left pending by a crashed process are delivered on next startup.
// Simulated by writing to a DB file, closing the store (crash), reopening it (restart),
// then driving one delivery cycle.
func TestAC2_PendingNotificationsDeliveredAfterRestart(t *testing.T) {
	delivered := make(chan string, 1)
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer vendor.Close()

	dbPath := filepath.Join(t.TempDir(), "restart_test.db")

	// Step 1: "original process" — enqueue then crash (close store without delivering).
	func() {
		s, err := notification.NewSQLiteStore(dbPath)
		if err != nil {
			t.Fatalf("open original store: %v", err)
		}
		defer s.Close()
		if _, err := notification.NewPersistingEnqueuer(s).Enqueue(
			"vendor-1", `{"event":"payment"}`, nil, vendor.URL, "POST",
		); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		// Store closes here — simulates process crash after persist but before delivery.
	}()

	// Step 2: "restarted process" — reopen the DB and process once.
	s2, err := notification.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open restart store: %v", err)
	}
	defer s2.Close()

	w := delivery.NewWorker(s2, delivery.DefaultPolicy, 5*time.Second, 1*time.Hour)
	if err := w.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}

	select {
	case <-delivered:
		// AC2 satisfied: notification was delivered after "restart"
	case <-time.After(3 * time.Second):
		t.Fatal("AC2 violated: notification not delivered after simulated restart")
	}
}

// WOR-13 AC3: each delivery attempt is logged with outcome including latency.
func TestAC3_DeliveryAttemptLoggedWithLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := newTestStore(t)
	rec := enqueueRecord(t, s, srv.URL)
	w := newTestWorker(t, s)
	runOnce(t, w)

	got := getRecord(t, s, rec.ID)
	if got.Status != notification.StatusDelivered {
		t.Fatalf("expected delivered, got %s", got.Status)
	}

	// Retrieve the delivery attempt and verify latency_ms is present.
	attempts, err := s.GetAttempts(rec.ID)
	if err != nil {
		t.Fatalf("GetAttempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(attempts))
	}
	a := attempts[0]
	if a.LatencyMs < 0 {
		t.Errorf("latency_ms should be non-negative, got %d", a.LatencyMs)
	}
	if a.Status != notification.AttemptSuccess {
		t.Errorf("expected success status, got %s", a.Status)
	}
	if a.HTTPStatus == nil || *a.HTTPStatus != http.StatusOK {
		t.Errorf("expected HTTP 200, got %v", a.HTTPStatus)
	}
}
