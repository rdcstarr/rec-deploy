package ui

import (
	"errors"
	"testing"
)

// TestSpinnerRunsAction checks that Spinner runs its action and propagates the
// result. go test's stderr is not a TTY, so this exercises the no-op path that
// callers rely on for piped/CI use.
func TestSpinnerRunsAction(t *testing.T) {
	want := errors.New("boom")
	called := false

	got := Spinner("working", func() error {
		called = true
		return want
	})

	if !called {
		t.Fatal("Spinner did not run the action")
	}
	if !errors.Is(got, want) {
		t.Errorf("Spinner returned %v, want %v", got, want)
	}
}
