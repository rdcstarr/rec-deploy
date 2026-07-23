package store

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrate applies every embedded migration not yet recorded, in version order.
// It is idempotent: already-applied migrations are skipped.
//
// Migrations must be additive, and TestMigrationsAreAdditive enforces it. Only
// the migrations this binary embeds are ever applied, and they are skipped by
// recorded version — nothing reads the highest version present — so an older
// binary opens a newer database and simply works. That is what makes
// self-update's rollback able to restore service instead of merely restoring a
// file: a v1 binary that refused the v2 schema would turn one bad release into a
// guaranteed outage on every server that took it, with nobody watching.
//
// The path that leans on this hardest is the least visible one. After a bad
// release, the restored v1 binary writes RecordBadTag into update_state, which is
// what stops the same release being reinstalled and rolled back every hour.
// Restructure that table in a future migration and the tag is never recorded and
// the loop never ends — it is the newest, least-settled table and the one with
// the nastiest failure.
func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return err
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		version, err := versionOf(name)
		if err != nil {
			return err
		}

		applied, err := isApplied(ctx, db, version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		script, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}

		if err := applyOne(ctx, db, version, string(script)); err != nil {
			return err
		}
	}

	return nil
}

// versionOf extracts the leading integer version from a migration filename
// such as "0001_init.sql".
func versionOf(name string) (int, error) {
	prefix, _, _ := strings.Cut(name, "_")

	return strconv.Atoi(prefix)
}

// isApplied reports whether the given migration version is already recorded.
func isApplied(ctx context.Context, db *sql.DB, version int) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count)

	return count > 0, err
}

// applyOne runs a migration's SQL and records its version in one transaction.
func applyOne(ctx context.Context, db *sql.DB, version int, script string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, script); err != nil {
		_ = tx.Rollback()

		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
		_ = tx.Rollback()

		return err
	}

	return tx.Commit()
}
