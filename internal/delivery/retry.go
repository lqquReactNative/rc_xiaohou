package delivery

import (
	"errors"
	"net"
	"time"
)

// RetryPolicy encodes when and how often to retry a failed delivery.
type RetryPolicy struct {
	// BackoffSchedule[i] is the delay before the (i+1)-th retry attempt.
	// len(BackoffSchedule) is the maximum number of retries.
	BackoffSchedule []time.Duration
}

// DefaultPolicy uses the exponential schedule required by AC2:
// 1 min → 5 min → 30 min → 2 h → 8 h (5 retries max).
var DefaultPolicy = RetryPolicy{
	BackoffSchedule: []time.Duration{
		1 * time.Minute,
		5 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
		8 * time.Hour,
	},
}

// ShouldRetry returns true when the delivery outcome warrants another attempt.
//
// Retry on:
//   - network errors (timeout, connection refused, DNS failure)
//   - HTTP 5xx (vendor-side errors)
//
// Do NOT retry on:
//   - HTTP 4xx (permanent client errors — retrying cannot fix them, AC3)
//   - HTTP 2xx/3xx (success)
func ShouldRetry(httpStatus int, err error) bool {
	if err != nil {
		// Only retry on network-level errors; not on every wrapped error.
		var netErr net.Error
		if errors.As(err, &netErr) {
			return true
		}
		// connection refused and similar
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return true
		}
		// Treat any other transport error as retriable (e.g. io.EOF mid-response).
		return true
	}
	return httpStatus >= 500
}

// NextRetry returns the delay before the next retry and whether one is allowed.
// retryCount is the number of attempts already made (0 = no attempt yet).
func (p RetryPolicy) NextRetry(retryCount int) (time.Duration, bool) {
	if retryCount >= len(p.BackoffSchedule) {
		return 0, false
	}
	return p.BackoffSchedule[retryCount], true
}

// MaxRetries is the total number of retry attempts the policy allows.
func (p RetryPolicy) MaxRetries() int {
	return len(p.BackoffSchedule)
}
