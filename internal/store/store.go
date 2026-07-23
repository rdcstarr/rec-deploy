// Package store owns rec-deploy's local SQLite state: opening the database,
// running embedded migrations, and exposing typed access to the administered
// repositories and their deploy history. It uses the pure-Go
// modernc.org/sqlite driver so the binary stays static.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // register the pure-Go SQLite driver
)

// Store wraps the application's SQLite database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path, applies
// pragmatic single-writer settings, runs migrations, and returns the Store.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// The daemon and the CLI are single-writer processes; one connection avoids
	// SQLite lock churn, and it keeps the foreign_keys pragma on every query.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	// PingContext forced the file (and its WAL sidecars) into existence; the
	// driver creates them with the process umask default (typically 0o644), so
	// restrict them to the owner — the DB holds every repository's webhook HMAC
	// secret and URL token.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Chmod(p, 0o600)
	}

	if err := migrate(ctx, db); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &Store{db: db}, nil
}

// OpenReadOnly opens an existing state database without creating files,
// changing permissions, or applying migrations. It is for reporting surfaces
// that promise not to modify the server, such as the MCP server.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("ping sqlite read-only: %w", err)
	}

	return &Store{db: db}, nil
}

// DB returns the underlying database handle for feature modules.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close closes the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}
