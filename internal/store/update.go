package store

import (
	"context"
	"fmt"
)

// LastBadTag returns the release tag last recorded as having failed to stay up
// after an unattended update, or "" if none. The updater skips reinstalling it
// so a bad release is not retried and rolled back on every tick.
func (s *Store) LastBadTag(ctx context.Context) (string, error) {
	var tag string
	if err := s.db.QueryRowContext(ctx, `SELECT bad_tag FROM update_state WHERE id = 1`).Scan(&tag); err != nil {
		return "", fmt.Errorf("read last bad release tag: %w", err)
	}

	return tag, nil
}

// RecordBadTag remembers tag as a release that failed its health check, so the
// next tick does not reinstall it. It overwrites any previous value: only the
// most recent bad tag matters, because a newer release always supersedes it.
func (s *Store) RecordBadTag(ctx context.Context, tag string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE update_state SET bad_tag = ?, recorded_at = datetime('now') WHERE id = 1`, tag); err != nil {
		return fmt.Errorf("record bad release tag %s: %w", tag, err)
	}

	return nil
}
