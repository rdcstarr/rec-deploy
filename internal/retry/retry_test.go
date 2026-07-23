package retry

import (
	"context"
	"errors"
	"testing"
)

func TestDoRetriesUntilSuccess(t *testing.T) {
	calls := 0
	err := Do(context.Background(), 5, func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestDoExhaustsAttempts(t *testing.T) {
	calls := 0
	err := Do(context.Background(), 2, func() error {
		calls++
		return errors.New("always fails")
	})
	if err == nil {
		t.Fatal("Do: want error after exhausting attempts")
	}
	if calls != 3 { // initial attempt + 2 retries
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestDoPermanentStopsEarly(t *testing.T) {
	sentinel := errors.New("fatal")
	calls := 0
	err := Do(context.Background(), 5, func() error {
		calls++
		return Permanent(sentinel)
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (Permanent stops immediately)", calls)
	}
}

// TestDoStopsOnContextCancel pins the documented "honors ctx cancellation
// between attempts" contract — the Ctrl+C path threaded through every retrying
// caller. It fails if backoff.WithContext is ever dropped from Do.
func TestDoStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := Do(ctx, 5, func() error {
		calls++
		cancel() // simulate Ctrl+C after the first failing attempt
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls > 2 {
		t.Errorf("calls = %d, want <= 2 (must stop promptly after cancel)", calls)
	}
}
