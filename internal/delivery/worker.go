package delivery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/lqquReactNative/rc_xiaohou/internal/notification"
)

// Worker polls the notification store for pending records and delivers them.
type Worker struct {
	store    notification.DeliveryStore
	policy   RetryPolicy
	client   *http.Client
	interval time.Duration
	logger   *slog.Logger
}

func NewWorker(s notification.DeliveryStore, policy RetryPolicy, timeout, interval time.Duration) *Worker {
	return &Worker{
		store:    s,
		policy:   policy,
		client:   &http.Client{Timeout: timeout},
		interval: interval,
		logger:   slog.Default(),
	}
}

// NewWorkerWithClient creates a Worker with a caller-supplied HTTP client.
// Useful in tests to inject a custom client without needing a timeout duration.
func NewWorkerWithClient(s notification.DeliveryStore, policy RetryPolicy, client *http.Client) *Worker {
	return &Worker{
		store:    s,
		policy:   policy,
		client:   client,
		interval: 10 * time.Second,
		logger:   slog.Default(),
	}
}

// Run starts the delivery loop and blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.ProcessOnce(ctx); err != nil {
				w.logger.Error("delivery cycle error", "err", err)
			}
		}
	}
}

// ProcessOnce is exported so tests can drive a single delivery cycle directly.
func (w *Worker) ProcessOnce(ctx context.Context) error {
	records, err := w.store.ClaimPending(50)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	for _, r := range records {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.deliver(r)
	}
	return nil
}

func (w *Worker) deliver(r *notification.Record) {
	attemptNumber := r.RetryCount + 1
	start := time.Now()
	result := w.send(r)
	latencyMs := time.Since(start).Milliseconds()

	// Build attempt record
	var httpStatusPtr *int
	var errMsgPtr *string
	if result.httpStatus != 0 {
		httpStatusPtr = &result.httpStatus
	}
	if result.transportErr != nil {
		msg := result.transportErr.Error()
		errMsgPtr = &msg
	} else if result.httpStatus >= 400 {
		msg := fmt.Sprintf("vendor returned %d", result.httpStatus)
		errMsgPtr = &msg
	}

	isSuccess := result.transportErr == nil && result.httpStatus >= 200 && result.httpStatus < 300
	attemptStatus := notification.AttemptFailed
	if isSuccess {
		attemptStatus = notification.AttemptSuccess
	}
	if err := w.store.RecordAttempt(r.ID, attemptNumber, attemptStatus, httpStatusPtr, errMsgPtr, latencyMs); err != nil {
		w.logger.Error("record attempt", "err", err)
	}

	if isSuccess {
		_ = w.store.MarkDelivered(r.ID)
		w.logger.Info("delivered", "notification_id", r.ID, "attempt", attemptNumber)
		return
	}

	// ShouldRetry uses httpStatus=0 when transportErr != nil (no HTTP response received)
	if !ShouldRetry(result.httpStatus, result.transportErr) {
		// 4xx or other non-retriable error — permanent failure, no retry
		w.logger.Warn("permanent failure, not retrying",
			"notification_id", r.ID, "http_status", result.httpStatus, "attempt", attemptNumber)
		_ = w.store.MarkFailed(r.ID)
		return
	}

	delay, canRetry := w.policy.NextRetry(r.RetryCount)
	if !canRetry {
		// AC2 (WOR-15): structured ERROR alert on DLQ insertion so operators are
		// notified promptly that manual intervention is needed.
		_ = w.store.MarkDead(r.ID)
		w.logger.Error("notification moved to dead-letter queue",
			"notification_id", r.ID,
			"vendor_id", r.VendorID,
			"attempts", attemptNumber,
			"last_http_status", result.httpStatus,
		)
		return
	}

	nextRetryAt := time.Now().Add(delay)
	_ = w.store.ScheduleRetry(r.ID, r.RetryCount+1, nextRetryAt)
	w.logger.Info("scheduled retry",
		"notification_id", r.ID, "attempt", attemptNumber,
		"next_retry_at", nextRetryAt, "delay", delay)
}

// sendResult holds the outcome of a single outbound delivery attempt.
type sendResult struct {
	httpStatus   int   // 0 if no HTTP response was received
	transportErr error // non-nil only for network-level failures
}

// send performs the outbound HTTP request.
// transportErr is set only on network-level failures (no response received).
func (w *Worker) send(r *notification.Record) sendResult {
	req, err := http.NewRequest(r.Method, r.TargetURL, bytes.NewReader([]byte(r.RenderedBody)))
	if err != nil {
		return sendResult{transportErr: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return sendResult{transportErr: err}
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()
	return sendResult{httpStatus: resp.StatusCode}
}
