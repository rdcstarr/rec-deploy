package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// Repo is a repository rec-deploy administers: its webhook URL token, its HMAC
// secret, and the IDs of the deploy key and webhook it created on GitHub.
type Repo struct {
	ID           int64
	Repository   string // owner/repo
	Token        string // the /hook/{token} path segment
	Secret       string // HMAC-SHA256 webhook secret (sensitive)
	GitHubKeyID  int64
	GitHubHookID int64
	CreatedAt    time.Time
}

// repoColumns is the shared SELECT list, so every scanner stays in step.
const repoColumns = `id, repository, token, secret, github_key_id, github_hook_id, created_at`

// scanRepo reads one repos row.
func scanRepo(row interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	var created string

	if err := row.Scan(&r.ID, &r.Repository, &r.Token, &r.Secret, &r.GitHubKeyID, &r.GitHubHookID, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Repo{}, ErrNotFound
		}

		return Repo{}, err
	}
	r.CreatedAt, _ = time.Parse(time.DateTime, created)

	return r, nil
}

// RepoInsert stores a repository and returns its row ID.
func (s *Store) RepoInsert(ctx context.Context, r Repo) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO repos (repository, token, secret, github_key_id, github_hook_id) VALUES (?, ?, ?, ?, ?)`,
		r.Repository, r.Token, r.Secret, r.GitHubKeyID, r.GitHubHookID)
	if err != nil {
		return 0, fmt.Errorf("insert repo %s: %w", r.Repository, err)
	}

	return res.LastInsertId()
}

// RepoUpdate rewrites a repository's rotatable fields (token, secret and the
// GitHub key/hook IDs), matched on its ID.
func (s *Store) RepoUpdate(ctx context.Context, r Repo) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE repos SET token = ?, secret = ?, github_key_id = ?, github_hook_id = ? WHERE id = ?`,
		r.Token, r.Secret, r.GitHubKeyID, r.GitHubHookID, r.ID)
	if err != nil {
		return fmt.Errorf("update repo %d: %w", r.ID, err)
	}

	return nil
}

// RepoDelete removes a repository and, through the foreign keys, its deploys.
func (s *Store) RepoDelete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete repo %d: %w", id, err)
	}

	return nil
}

// RepoByToken looks a repository up by its webhook URL token. It returns
// ErrNotFound when the token is unknown — the webhook handler answers 404.
func (s *Store) RepoByToken(ctx context.Context, token string) (Repo, error) {
	return scanRepo(s.db.QueryRowContext(ctx, `SELECT `+repoColumns+` FROM repos WHERE token = ?`, token))
}

// RepoByName looks a repository up by its owner/repo slug.
func (s *Store) RepoByName(ctx context.Context, repository string) (Repo, error) {
	return scanRepo(s.db.QueryRowContext(ctx, `SELECT `+repoColumns+` FROM repos WHERE repository = ?`, repository))
}

// Repos lists every administered repository, by slug.
func (s *Store) Repos(ctx context.Context) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+repoColumns+` FROM repos ORDER BY repository`)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}

	return out, rows.Err()
}
