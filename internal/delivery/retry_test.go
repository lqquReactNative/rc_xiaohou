package delivery_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lqquReactNative/rc_xiaohou/internal/delivery"
	"github.com/lqquReactNative/rc_xiaohou/internal/notification"
)

// --- Unit tests for ShouldRetry ---

func TestShouldRetry_5xx(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		if !delivery.ShouldRetry(code, nil) {
			t.Errorf("expected retry for HTTP %d", code)
		}
	}
}

func TestShouldRetry_NetworkError(t *testing.T) {
	err := &net.OpError{Op: "dial", Err: fmt.Errorf("connection refused")}
	if !delivery.ShouldRetry(0, err) {
		t.Error("expected retry on network error")
	}
}

// AC3: 4xx must NOT be retried.
func TestShouldRetry_4xx_NoRetry(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 422} {
		if delivery.ShouldRetry(code, nil) {
			t.Errorf("expected NO retry for HTTP %d (AC3)", code)
		}
	}
}

func TestShouldRetry_2xx_NoRetry(t *testing.T) {
	for _, code := range []int{200, 201, 204} {
		if delivery.ShouldRetry(code, nil) {
			t.Errorf("expected NO retry for HTTP %d", code)
		}
	}
}

// --- Unit tests for RetryPolicy.NextRetry ---

// AC2: retry delays must follow the exponential schedule.
func TestRetryPolicy_BackoffSchedule(t *testing.T) {
	p := delivery.DefaultPolicy
	expected := []time.Duration{
		1 * time.Minute,
		5 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
		8 * time.Hour,
	}
	for i, want := range expected {
		got, ok := p.NextRetry(i)
		if !ok {
			t.Fatalf("NextRetry(%d): expected a delay, got none", i)
		}
		if got != want {
			t.Errorf("NextRetry(%d): got %v, want %v", i, got, want)
		}
	}
	// After the last slot there should be no more retries.
	_, ok := p.NextRetry(len(expected))
	if ok {
		t.Error("expected no retry after schedule exhausted")
	}
}

func TestRetryPolicy_MaxRetries(t *testing.T) {
	if delivery.DefaultPolicy.MaxRetries() != 5 {
		t.Errorf("expected MaxRetries=5, got %d", delivery.DefaultPolicy.MaxRetries())
	}
}

// --- Integration tests using SQLiteStore and a test HTTP server ---

func newTestStore(t *testing.T) *notification.SQLiteStore {
	t.Helper()
	s, err := notification.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func enqueueRecord(t *testing.T, s *notification.SQLiteStore, targetURL string) *notification.Record {
	t.Helper()
	ctx := context.Background()
	r := &notification.Record{
		ID:           fmt.Sprintf("test-%d", time.Now().UnixNano()),
		VendorID:     "test-vendor",
		RenderedBody: `{"event":"test"}`,
		Headers:      map[string]string{},
		TargetURL:    targetURL,
		Method:       "POST",
		Status:       notification.StatusPending,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.Save(ctx, r); err != nil {
		t.Fatalf("save record: %v", err)
	}
	return r
}

func newTestWorker(t *testing.T, s notification.DeliveryStore) *delivery.Worker {
	t.Helper()
	policy := delivery.RetryPolicy{
		BackoffSchedule: []time.Duration{
			1 * time.Millisecond,
			2 * time.Millisecond,
			4 * time.Millisecond,
			8 * time.Millisecond,
			16 * time.Millisecond,
		},
	}
	return delivery.NewWorker(s, policy, 5*time.Second, 1*time.Hour)
}

func runOnce(t *testing.T, w *delivery.Worker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.ProcessOnce(ctx); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
}

func getRecord(t *testing.T, s *notification.SQLiteStore, id string) *notification.Record {
	t.Helper()
	r, err := s.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	return r
}

// AC1: When vendor returns 5xx, the notification must be automatically rescheduled.
func TestAC1_5xxTriggersRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // 503
	}))
	defer srv.Close()

	s := newTestStore(t)
	rec := enqueueRecord(t, s, srv.URL)
	w := newTestWorker(t, s)
	runOnce(t, w)

	got := getRecord(t, s, rec.ID)
	if got.Status != notification.StatusPending {
		t.Errorf("AC1: expected status=pending after 5xx, got %q", got.Status)
	}
	if got.RetryCount != 1 {
		t.Errorf("AC1: expected retry_count=1, got %d", got.RetryCount)
	}
	if got.NextRetryAt == nil {
		t.Error("AC1: expected next_retry_at to be set")
	}
}

// AC1 (network error variant): connection refused triggers retry.
func TestAC1_NetworkErrorTriggersRetry(t *testing.T) {
	s := newTestStore(t)
	rec := enqueueRecord(t, s, "http://127.0.0.1:1") // port 1 always refused
	w := newTestWorker(t, s)
	runOnce(t, w)

	got := getRecord(t, s, rec.ID)
	if got.Status != notification.StatusPending {
		t.Errorf("AC1(network): expected status=pending after connection refused, got %q", got.Status)
	}
	if got.RetryCount != 1 {
		t.Errorf("AC1(network): expected retry_count=1, got %d", got.RetryCount)
	}
}

// AC2: Verify the default backoff schedule matches the spec exactly.
func TestAC2_RetryDelaysAreExponential(t *testing.T) {
	p := delivery.DefaultPolicy
	schedule := []time.Duration{1 * time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 8 * time.Hour}
	for i, want := range schedule {
		got, ok := p.NextRetry(i)
		if !ok || got != want {
			t.Errorf("AC2: delay[%d] = %v, want %v (ok=%v)", i, got, want, ok)
		}
	}
}

// AC3: When vendor returns 4xx, the notification must NOT be retried.
func TestAC3_4xxNoRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest) // 400
	}))
	defer srv.Close()

	s := newTestStore(t)
	rec := enqueueRecord(t, s, srv.URL)
	w := newTestWorker(t, s)
	runOnce(t, w)

	got := getRecord(t, s, rec.ID)
	if got.Status != notification.StatusFailed {
		t.Errorf("AC3: expected status=failed after 4xx, got %q", got.Status)
	}
	if got.RetryCount != 0 {
		t.Errorf("AC3: expected retry_count=0, got %d", got.RetryCount)
	}
}

// Successful delivery marks the notification as delivered.
func TestSuccessfulDelivery(t *testing.T) {
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
		t.Errorf("expected delivered, got %q", got.Status)
	}
}

// After exhausting all retries the notification moves to dead-letter state.
func TestRetryBudgetExhausted_MovesToDead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := newTestStore(t)
	rec := enqueueRecord(t, s, srv.URL)

	// Use zero-duration backoffs so we don't have to sleep.
	policy := delivery.RetryPolicy{
		BackoffSchedule: make([]time.Duration, delivery.DefaultPolicy.MaxRetries()),
	}
	w := delivery.NewWorker(s, policy, 5*time.Second, 1*time.Hour)
	ctx := context.Background()

	maxAttempts := delivery.DefaultPolicy.MaxRetries() + 1
	for i := 0; i < maxAttempts; i++ {
		if err := s.ResetNextRetryAt(rec.ID); err != nil {
			t.Fatalf("ResetNextRetryAt iter %d: %v", i, err)
		}
		if err := w.ProcessOnce(ctx); err != nil {
			t.Fatalf("ProcessOnce iter %d: %v", i, err)
		}
	}

	got := getRecord(t, s, rec.ID)
	if got.Status != notification.StatusDead {
		t.Errorf("expected dead after exhausting retries, got %q", got.Status)
	}
}
