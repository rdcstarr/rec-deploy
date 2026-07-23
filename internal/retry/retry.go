// Package retry runs idempotent operations with one shared exponential-backoff
// policy, so every component retries transient failures the same way instead of
// each hand-rolling its own backoff.
package retry

import (
	"context"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// Default is the usual number of extra attempts for a one-off lookup.
const Default = 3

// Do runs op with exponential backoff (300ms → 5s), retrying up to attempts
// extra times whenever op returns a non-nil error. Return nil to stop — on
// success, or on a non-retryable outcome the caller handles itself. Wrap an
// error with Permanent to stop immediately without further attempts (e.g. a 4xx
// response). It honors ctx cancellation between attempts.
func Do(ctx context.Context, attempts int, op func() error) error {
	if attempts < 0 {
		attempts = 0
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 300 * time.Millisecond
	bo.MaxInterval = 5 * time.Second

	return backoff.Retry(op, backoff.WithContext(backoff.WithMaxRetries(bo, uint64(attempts)), ctx))
}

// Permanent wraps err so Do stops immediately, without retrying — for fatal
// errors such as a 4xx response or invalid input.
func Permanent(err error) error {
	return backoff.Permanent(err)
}
