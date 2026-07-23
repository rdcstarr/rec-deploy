package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// open returns a Store backed by a fresh database in the test's temp dir.
func open(t *testing.T) *Store {
	t.Helper()

	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

func TestOpenIsIdempotentAndOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	for i := 0; i < 2; i++ {
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i, err)
		}
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600 — the db holds webhook secrets", perm)
	}
}

func TestRepoCRUD(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	id, err := s.RepoInsert(ctx, Repo{Repository: "rdcstarr/tema", Token: "tok", Secret: "sec", GitHubKeyID: 1, GitHubHookID: 2})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	byToken, err := s.RepoByToken(ctx, "tok")
	if err != nil {
		t.Fatalf("RepoByToken: %v", err)
	}
	if byToken.ID != id || byToken.Secret != "sec" {
		t.Errorf("RepoByToken = %+v, want id %d", byToken, id)
	}

	if _, err := s.RepoByToken(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RepoByToken(unknown) err = %v, want ErrNotFound", err)
	}

	byName, err := s.RepoByName(ctx, "rdcstarr/tema")
	if err != nil || byName.ID != id {
		t.Fatalf("RepoByName = %+v, %v", byName, err)
	}

	all, err := s.Repos(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("Repos = %v, %v; want 1 row", all, err)
	}

	if err := s.RepoDelete(ctx, id); err != nil {
		t.Fatalf("RepoDelete: %v", err)
	}
	if all, _ := s.Repos(ctx); len(all) != 0 {
		t.Errorf("Repos after delete = %v, want empty", all)
	}
}

// RepoUpdate is how `rec-deploy repo rotate` swaps the token, the HMAC secret and
// the GitHub key/hook IDs; the old token must stop resolving.
func TestRepoUpdateRotatesCredentials(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	id, err := s.RepoInsert(ctx, Repo{Repository: "rdcstarr/tema", Token: "old", Secret: "old-sec"})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	err = s.RepoUpdate(ctx, Repo{ID: id, Token: "new", Secret: "new-sec", GitHubKeyID: 7, GitHubHookID: 9})
	if err != nil {
		t.Fatalf("RepoUpdate: %v", err)
	}

	if _, err := s.RepoByToken(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old token still resolves: err = %v, want ErrNotFound", err)
	}

	r, err := s.RepoByToken(ctx, "new")
	if err != nil {
		t.Fatalf("RepoByToken(new): %v", err)
	}
	if r.Secret != "new-sec" || r.GitHubKeyID != 7 || r.GitHubHookID != 9 {
		t.Errorf("RepoByToken(new) = %+v, want rotated secret and IDs", r)
	}
}

// A replayed webhook must be a no-op, not a second deploy. This is the replay
// hole an old implementation leaves open: it stores X-GitHub-Delivery and never checks it.
func TestDeployStartDeduplicatesDelivery(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	repoID, err := s.RepoInsert(ctx, Repo{Repository: "rdcstarr/tema", Token: "tok", Secret: "sec"})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	d := Deploy{RepoID: repoID, DeliveryID: "delivery-1", Ref: "refs/heads/main", SHA: "abc", Status: StatusRunning}
	if _, err := s.DeployStart(ctx, d); err != nil {
		t.Fatalf("DeployStart: %v", err)
	}

	if _, err := s.DeployStart(ctx, d); !errors.Is(err, ErrDuplicateDelivery) {
		t.Fatalf("second DeployStart err = %v, want ErrDuplicateDelivery", err)
	}
}

// Manual deploys carry no delivery ID; several of them must not collide.
func TestDeployStartAllowsManyManualDeploys(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	repoID, err := s.RepoInsert(ctx, Repo{Repository: "rdcstarr/tema", Token: "tok", Secret: "sec"})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	for i := 0; i < 2; i++ {
		if _, err := s.DeployStart(ctx, Deploy{RepoID: repoID, Ref: "refs/heads/main", SHA: "abc", Status: StatusRunning}); err != nil {
			t.Fatalf("manual DeployStart #%d: %v", i, err)
		}
	}
}

func TestOpenReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	if _, err := OpenReadOnly(ctx, path); err == nil {
		t.Fatal("OpenReadOnly created a missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing database stat error = %v, want not exist", err)
	}

	writable, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := writable.RepoInsert(ctx, Repo{Repository: "owner/repo"}); err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}
	if err := writable.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	readonly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = readonly.Close() }()
	repos, err := readonly.Repos(ctx)
	if err != nil || len(repos) != 1 || repos[0].Repository != "owner/repo" {
		t.Fatalf("Repos = %+v, %v", repos, err)
	}
	if _, err := readonly.RepoInsert(ctx, Repo{Repository: "other/repo"}); err == nil {
		t.Fatal("RepoInsert through read-only store succeeded")
	}
}

func TestDeployPathsAndHistory(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	repoID, err := s.RepoInsert(ctx, Repo{Repository: "rdcstarr/tema", Token: "tok", Secret: "sec"})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	id, err := s.DeployStart(ctx, Deploy{RepoID: repoID, DeliveryID: "d1", Ref: "refs/heads/main", SHA: "abc", Status: StatusRunning})
	if err != nil {
		t.Fatalf("DeployStart: %v", err)
	}

	err = s.DeployPathInsert(ctx, DeployPath{
		DeployID: id, Path: "/var/www/api", User: "root", RanAsRoot: true,
		PreviousSHA: "old", NewSHA: "abc", Status: StatusSuccess, Commands: `[{"command":"true","exit_code":0}]`,
	})
	if err != nil {
		t.Fatalf("DeployPathInsert: %v", err)
	}
	if err := s.DeployFinish(ctx, id, StatusSuccess); err != nil {
		t.Fatalf("DeployFinish: %v", err)
	}

	history, err := s.Deploys(ctx, "rdcstarr/tema", 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("Deploys = %v, %v; want 1", history, err)
	}
	if history[0].Status != StatusSuccess || history[0].FinishedAt.IsZero() {
		t.Errorf("deploy not finished: %+v", history[0])
	}
	got, err := s.DeployByID(ctx, id)
	if err != nil {
		t.Fatalf("DeployByID: %v", err)
	}
	if got.ID != id || got.SHA != "abc" || got.Status != StatusSuccess {
		t.Errorf("DeployByID = %+v", got)
	}
	if _, err := s.DeployByID(ctx, id+1); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeployByID missing err = %v, want ErrNotFound", err)
	}

	paths, err := s.DeployPaths(ctx, id)
	if err != nil || len(paths) != 1 {
		t.Fatalf("DeployPaths = %v, %v; want 1", paths, err)
	}
	if !paths[0].RanAsRoot {
		t.Error("RanAsRoot lost in round trip — root deploys must stay visible")
	}

	last, err := s.LastDeployPerPath(ctx)
	if err != nil || len(last) != 1 || last[0].Path != "/var/www/api" {
		t.Fatalf("LastDeployPerPath = %v, %v", last, err)
	}
}

// Deleting a repository must take its deploy history with it: the rows hold the
// commit messages and authors of a repo rec-deploy no longer administers.
func TestRepoDeleteCascadesDeploys(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	repoID, err := s.RepoInsert(ctx, Repo{Repository: "rdcstarr/tema", Token: "tok", Secret: "sec"})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	id, err := s.DeployStart(ctx, Deploy{RepoID: repoID, DeliveryID: "d1", Ref: "refs/heads/main", SHA: "abc", Status: StatusRunning})
	if err != nil {
		t.Fatalf("DeployStart: %v", err)
	}
	if err := s.DeployPathInsert(ctx, DeployPath{DeployID: id, Path: "/var/www/api", Status: StatusSuccess}); err != nil {
		t.Fatalf("DeployPathInsert: %v", err)
	}

	if err := s.RepoDelete(ctx, repoID); err != nil {
		t.Fatalf("RepoDelete: %v", err)
	}

	if history, err := s.Deploys(ctx, "", 10); err != nil || len(history) != 0 {
		t.Errorf("Deploys after repo delete = %v, %v; want none", history, err)
	}
	if paths, err := s.DeployPaths(ctx, id); err != nil || len(paths) != 0 {
		t.Errorf("DeployPaths after repo delete = %v, %v; want none", paths, err)
	}
}

// A hard kill — SIGKILL, an OOM during a build, power loss — leaves a deploy row
// marked running forever. Nothing else can correct it: the delivery id is spent,
// so GitHub's Redeliver is a no-op 200 against the dedup index. The row then lies
// in `logs` and `status` for the life of the database.
func TestReconcileInterruptedSettlesStrandedWebhookDeploys(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	repoID, err := s.RepoInsert(ctx, Repo{Repository: "rdcstarr/tema", Token: "tok", Secret: "sec"})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	// A webhook deploy the process never finished, and a manual one.
	if _, err := s.DeployStart(ctx, Deploy{RepoID: repoID, DeliveryID: "d1", Ref: "refs/heads/main", SHA: "abc", Status: StatusRunning}); err != nil {
		t.Fatalf("DeployStart webhook: %v", err)
	}
	manual, err := s.DeployStart(ctx, Deploy{RepoID: repoID, Ref: "refs/heads/main", SHA: "def", Status: StatusRunning})
	if err != nil {
		t.Fatalf("DeployStart manual: %v", err)
	}

	n, err := s.ReconcileInterrupted(ctx)
	if err != nil {
		t.Fatalf("ReconcileInterrupted: %v", err)
	}
	if n != 1 {
		t.Errorf("stamped %d deploys, want 1", n)
	}

	deploys, err := s.Deploys(ctx, "rdcstarr/tema", 10)
	if err != nil {
		t.Fatalf("Deploys: %v", err)
	}
	for _, d := range deploys {
		switch {
		case d.DeliveryID == "d1" && d.Status != StatusInterrupted:
			t.Errorf("the stranded webhook deploy is still %q — it lies in logs forever", d.Status)
		case d.ID == manual && d.Status != StatusRunning:
			t.Errorf("a manual deploy was stamped %q — nobody asked the daemon to rule on it", d.Status)
		}
	}

	// Idempotent: a second start has nothing left to settle.
	if n, err := s.ReconcileInterrupted(ctx); err != nil || n != 0 {
		t.Errorf("second ReconcileInterrupted = %d, %v — want 0, nil", n, err)
	}
}

// LastDeployPerPathIn is what rollback falls back on when the newest deploy was
// hard-killed before it recorded any path. It must see only the named repository:
// resetting one repo's checkout to another's commit is the worst possible outcome
// of a recovery command.
func TestLastDeployPerPathInIsScopedToOneRepository(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	mine, err := s.RepoInsert(ctx, Repo{Repository: "rdcstarr/tema", Token: "tok-tema", Secret: "s"})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}
	other, err := s.RepoInsert(ctx, Repo{Repository: "rdcstarr/other", Token: "tok-other", Secret: "s"})
	if err != nil {
		t.Fatalf("RepoInsert other: %v", err)
	}

	for _, tc := range []struct {
		repo                   int64
		delivery, path, newSHA string
	}{
		{mine, "d1", "/var/www/tema", "aaa"},
		{other, "d2", "/var/www/other", "zzz"},
	} {
		id, err := s.DeployStart(ctx, Deploy{RepoID: tc.repo, DeliveryID: tc.delivery, Ref: "refs/heads/main", SHA: tc.newSHA, Status: StatusRunning})
		if err != nil {
			t.Fatalf("DeployStart: %v", err)
		}
		if err := s.DeployPathInsert(ctx, DeployPath{DeployID: id, Path: tc.path, NewSHA: tc.newSHA, Status: StatusSuccess}); err != nil {
			t.Fatalf("DeployPathInsert: %v", err)
		}
	}

	got, err := s.LastDeployPerPathIn(ctx, "rdcstarr/tema")
	if err != nil {
		t.Fatalf("LastDeployPerPathIn: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d paths, want 1: %+v", len(got), got)
	}
	if got[0].Path != "/var/www/tema" || got[0].NewSHA != "aaa" {
		t.Errorf("got %+v — another repository's checkout leaked into the rollback target", got[0])
	}
}

// LastDeployPerPathIn excludes rows with no new_sha, and that exclusion is
// load-bearing: the branch filter writes a skipped row with empty SHAs, and if
// that row — routinely a path's newest — reached the caller, the rollback that
// reads it would treat "not moved" as a position and reset a tree onto a sibling's
// commit. This pins the filter the query comments call "the whole subtlety".
func TestLastDeployPerPathInExcludesRowsWithNoNewSHA(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	repoID, err := s.RepoInsert(ctx, Repo{Repository: "o/r", Token: "tok", Secret: "sec"})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	// prod was moved by a real deploy: a row with a new_sha.
	moved, err := s.DeployStart(ctx, Deploy{RepoID: repoID, DeliveryID: "d1", Ref: "refs/heads/main", SHA: "P", Status: StatusRunning})
	if err != nil {
		t.Fatalf("DeployStart: %v", err)
	}
	if err := s.DeployPathInsert(ctx, DeployPath{DeployID: moved, Path: "/var/www/prod", PreviousSHA: "P0", NewSHA: "P", Status: StatusSuccess}); err != nil {
		t.Fatalf("DeployPathInsert moved: %v", err)
	}

	// A later push to develop skipped prod: a NEWER row for the same path, with no
	// SHAs. It must not shadow the real one.
	skipped, err := s.DeployStart(ctx, Deploy{RepoID: repoID, DeliveryID: "d2", Ref: "refs/heads/develop", SHA: "Q", Status: StatusRunning})
	if err != nil {
		t.Fatalf("DeployStart: %v", err)
	}
	if err := s.DeployPathInsert(ctx, DeployPath{DeployID: skipped, Path: "/var/www/prod", Status: StatusSkipped}); err != nil {
		t.Fatalf("DeployPathInsert skipped: %v", err)
	}

	got, err := s.LastDeployPerPathIn(ctx, "o/r")
	if err != nil {
		t.Fatalf("LastDeployPerPathIn: %v", err)
	}
	if len(got) != 1 || got[0].NewSHA != "P" {
		t.Fatalf("got %+v, want the moved row (new_sha P) — the newer empty-sha row must be excluded", got)
	}
}
