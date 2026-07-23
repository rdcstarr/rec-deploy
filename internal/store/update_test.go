package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()

	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	return st
}

// TestLastBadTagDefaultsToEmpty: a fresh database has never seen a bad release,
// so the updater has nothing to skip.
func TestLastBadTagDefaultsToEmpty(t *testing.T) {
	st := openTestStore(t)

	tag, err := st.LastBadTag(context.Background())
	if err != nil {
		t.Fatalf("LastBadTag: %v", err)
	}
	if tag != "" {
		t.Errorf("LastBadTag on a fresh db = %q, want empty", tag)
	}
}

// TestRecordBadTagRoundTrips, and a newer bad tag overwrites the older one —
// only the most recent matters, because tags only move forward.
func TestRecordBadTagRoundTrips(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	if err := st.RecordBadTag(ctx, "v0.2.0"); err != nil {
		t.Fatalf("RecordBadTag: %v", err)
	}
	got, err := st.LastBadTag(ctx)
	if err != nil {
		t.Fatalf("LastBadTag: %v", err)
	}
	if got != "v0.2.0" {
		t.Errorf("LastBadTag = %q, want v0.2.0", got)
	}

	if err := st.RecordBadTag(ctx, "v0.3.0"); err != nil {
		t.Fatalf("RecordBadTag: %v", err)
	}
	got, err = st.LastBadTag(ctx)
	if err != nil {
		t.Fatalf("LastBadTag: %v", err)
	}
	if got != "v0.3.0" {
		t.Errorf("LastBadTag after overwrite = %q, want v0.3.0", got)
	}
}
