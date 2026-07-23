package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Deploy statuses.
const (
	StatusRunning    = "running"
	StatusSuccess    = "success"
	StatusFailed     = "failed"
	StatusSkipped    = "skipped"
	StatusRolledBack = "rolled_back"
	// StatusInterrupted marks a deploy the process never got to finish: only a
	// hard kill produces it, since every graceful path records on a context that
	// survives cancellation.
	StatusInterrupted = "interrupted"
)

// ErrDuplicateDelivery reports that this X-GitHub-Delivery has already been
// handled. The webhook answers 200 and does no work: a replayed signed request
// must not re-deploy.
var ErrDuplicateDelivery = errors.New("duplicate delivery")

// Deploy is one push (or one manual run) fanned out over every installation.
type Deploy struct {
	ID         int64
	RepoID     int64
	DeliveryID string // empty for a manual deploy — stored as NULL
	Ref        string // refs/heads/main
	SHA        string
	Message    string
	Author     string
	Status     string
	StartedAt  time.Time
	FinishedAt time.Time
}

// DeployPath is one installation's outcome within a Deploy.
type DeployPath struct {
	ID          int64
	DeployID    int64
	Path        string
	User        string
	RanAsRoot   bool
	PreviousSHA string
	NewSHA      string
	Status      string
	Reason      string // why it was skipped, or how it failed
	Commands    string // JSON: per-command exit code, duration, output tail
}

// DeployStart records a deploy as running and returns its row ID. A repeated
// delivery ID yields ErrDuplicateDelivery — the unique index is the replay guard,
// and letting SQLite enforce it keeps the check atomic under concurrent pushes.
func (s *Store) DeployStart(ctx context.Context, d Deploy) (int64, error) {
	// A manual deploy has no delivery ID. It must be stored as NULL, not "":
	// the index is partial on NULL, so two empty strings would collide.
	var delivery any
	if d.DeliveryID != "" {
		delivery = d.DeliveryID
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO deploys (repo_id, delivery_id, ref, sha, message, author, status) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.RepoID, delivery, d.Ref, d.SHA, d.Message, d.Author, d.Status)
	if err != nil {
		// deploys_delivery_id is the only unique index this statement can trip.
		var serr *sqlite.Error
		if errors.As(err, &serr) && serr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			return 0, ErrDuplicateDelivery
		}

		return 0, fmt.Errorf("start deploy: %w", err)
	}

	return res.LastInsertId()
}

// DeployFinish stamps a deploy's terminal status and finish time.
func (s *Store) DeployFinish(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE deploys SET status = ?, finished_at = datetime('now') WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("finish deploy %d: %w", id, err)
	}

	return nil
}

// DeployPathInsert records one installation's result.
func (s *Store) DeployPathInsert(ctx context.Context, p DeployPath) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deploy_paths (deploy_id, path, run_as_user, ran_as_root, previous_sha, new_sha, status, reason, commands)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.DeployID, p.Path, p.User, p.RanAsRoot, p.PreviousSHA, p.NewSHA, p.Status, p.Reason, p.Commands)
	if err != nil {
		return fmt.Errorf("record deploy path %s: %w", p.Path, err)
	}

	return nil
}

// Deploys lists the most recent deploys, newest first, optionally filtered to
// one repository. limit <= 0 means 50.
func (s *Store) Deploys(ctx context.Context, repository string, limit int) ([]Deploy, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `SELECT d.id, d.repo_id, COALESCE(d.delivery_id, ''), d.ref, d.sha, d.message, d.author,
	                 d.status, d.started_at, COALESCE(d.finished_at, '')
	          FROM deploys d JOIN repos r ON r.id = d.repo_id`
	args := []any{}
	if repository != "" {
		query += ` WHERE r.repository = ?`
		args = append(args, repository)
	}
	query += ` ORDER BY d.started_at DESC, d.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list deploys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Deploy
	for rows.Next() {
		var d Deploy
		var started, finished string
		if err := rows.Scan(&d.ID, &d.RepoID, &d.DeliveryID, &d.Ref, &d.SHA, &d.Message, &d.Author,
			&d.Status, &started, &finished); err != nil {
			return nil, err
		}
		// A running deploy has no finish time; the zero Time is how callers see that.
		d.StartedAt, _ = time.Parse(time.DateTime, started)
		d.FinishedAt, _ = time.Parse(time.DateTime, finished)
		out = append(out, d)
	}

	return out, rows.Err()
}

// DeployByID returns one deploy by its database ID. It returns ErrNotFound when
// no deploy has that ID.
func (s *Store) DeployByID(ctx context.Context, id int64) (Deploy, error) {
	var d Deploy
	var started, finished string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, repo_id, COALESCE(delivery_id, ''), ref, sha, message, author,
		        status, started_at, COALESCE(finished_at, '')
		 FROM deploys WHERE id = ?`, id).Scan(
		&d.ID, &d.RepoID, &d.DeliveryID, &d.Ref, &d.SHA, &d.Message, &d.Author,
		&d.Status, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return Deploy{}, ErrNotFound
	}
	if err != nil {
		return Deploy{}, fmt.Errorf("get deploy %d: %w", id, err)
	}

	d.StartedAt, _ = time.Parse(time.DateTime, started)
	d.FinishedAt, _ = time.Parse(time.DateTime, finished)

	return d, nil
}

// DeployPaths lists every installation result of one deploy.
func (s *Store) DeployPaths(ctx context.Context, deployID int64) ([]DeployPath, error) {
	return scanPaths(s.db.QueryContext(ctx,
		`SELECT id, deploy_id, path, run_as_user, ran_as_root, previous_sha, new_sha, status, reason, commands
		 FROM deploy_paths WHERE deploy_id = ? ORDER BY path`, deployID))
}

// LastDeployPerPath returns the most recent result for each known path — what
// `rec-deploy status` shows.
func (s *Store) LastDeployPerPath(ctx context.Context) ([]DeployPath, error) {
	return scanPaths(s.db.QueryContext(ctx,
		`SELECT p.id, p.deploy_id, p.path, p.run_as_user, p.ran_as_root, p.previous_sha, p.new_sha, p.status, p.reason, p.commands
		 FROM deploy_paths p
		 JOIN (SELECT path, MAX(id) AS id FROM deploy_paths GROUP BY path) latest ON latest.id = p.id
		 ORDER BY p.path`))
}

// ReconcileInterrupted stamps every webhook deploy still marked running as
// interrupted, and reports how many it stamped.
//
// A deploy is only ever left running by a hard kill — SIGKILL, an OOM, power
// loss. Every graceful path records its result on a context that outlives the
// cancellation, so nothing else strands a row. Without this the row lies for
// good, in `rec-deploy logs` and in `status`, and the delivery cannot be replayed
// to correct it: its GUID is already spent, so GitHub's Redeliver answers a no-op
// 200.
//
// Manual deploys are left alone. They carry no delivery id, and `rec-deploy
// deploy` is run by someone watching it — the daemon has no business ruling on
// whether another process is mid-run.
//
// Call this only where a daemon starts, never from store.Open: twenty call sites
// open the store, and `rec-deploy logs` must not stamp a deploy that is running
// right now. Even at startup the claim is "nothing in this process is running
// it", not "nothing anywhere is": a second `serve` is a documented invocation and
// there is no daemon lock, so one can transiently mislabel the other's in-flight
// rows. That heals on its own — DeployFinish stamps by id regardless of the
// status it finds.
func (s *Store) ReconcileInterrupted(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE deploys SET status = ?, finished_at = datetime('now')
		  WHERE status = ? AND delivery_id IS NOT NULL`,
		StatusInterrupted, StatusRunning)
	if err != nil {
		return 0, fmt.Errorf("reconcile interrupted deploys: %w", err)
	}

	return res.RowsAffected()
}

// LastDeployPerPathIn returns, for each checkout of repository, the most recent
// result that actually left the tree somewhere — the newest row with a new_sha.
//
// It exists for the rollback of a hard-killed deploy. Path results are written
// only when a deploy finishes, so a SIGKILL — an OOM during a build, or systemd
// losing patience with a drain — leaves the newest deploy with no path rows at
// all, while the tree was already reset to the new commit. The last recorded
// new_sha for that path is then where the tree stood before the kill, which is
// what a rollback must reach for.
//
// Rows with no new_sha are excluded, and that is the whole subtlety. The branch
// filter writes a skipped row with both SHAs empty for every checkout the push
// did not match, so on a server running staging on develop beside production on
// main, *every* push leaves one — they are the common row, not a rare one, and
// they are routinely a path's newest. Let one through and it says nothing about
// where that tree is, but it still stands in for the path: the caller's ambiguity
// check reads an empty SHA as "no opinion" and skips it, so the disagreement it
// exists to catch never surfaces and one checkout's commit is applied to all of
// them. Excluding them here keeps that check answering the question it was
// written for.
func (s *Store) LastDeployPerPathIn(ctx context.Context, repository string) ([]DeployPath, error) {
	return scanPaths(s.db.QueryContext(ctx,
		`SELECT p.id, p.deploy_id, p.path, p.run_as_user, p.ran_as_root, p.previous_sha, p.new_sha, p.status, p.reason, p.commands
		 FROM deploy_paths p
		 JOIN (SELECT p2.path, MAX(p2.id) AS id
		         FROM deploy_paths p2
		         JOIN deploys d ON d.id = p2.deploy_id
		         JOIN repos r ON r.id = d.repo_id
		        WHERE r.repository = ? AND p2.new_sha != ''
		        GROUP BY p2.path) latest ON latest.id = p.id
		 ORDER BY p.path`, repository))
}

// scanPaths reads deploy_paths rows from an already-issued query.
func scanPaths(rows *sql.Rows, err error) ([]DeployPath, error) {
	if err != nil {
		return nil, fmt.Errorf("list deploy paths: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DeployPath
	for rows.Next() {
		var p DeployPath
		if err := rows.Scan(&p.ID, &p.DeployID, &p.Path, &p.User, &p.RanAsRoot,
			&p.PreviousSHA, &p.NewSHA, &p.Status, &p.Reason, &p.Commands); err != nil {
			return nil, err
		}
		out = append(out, p)
	}

	return out, rows.Err()
}
