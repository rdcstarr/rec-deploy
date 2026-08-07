package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/deploy"
	"github.com/rdcstarr/rec-deploy/internal/store"
)

// targetsFrom is the core mapping: a row with a commit becomes a target, a row
// without one does not, and --path keeps only the named checkout.
func TestTargetsFrom(t *testing.T) {
	paths := []store.DeployPath{
		{Path: "/srv/prod", PreviousSHA: "aaaaaaaaaaaa"},
		{Path: "/srv/staging", PreviousSHA: "bbbbbbbbbbbb"},
		{Path: "/srv/idle", PreviousSHA: ""}, // skipped by the branch filter
	}
	prev := func(p store.DeployPath) string { return p.PreviousSHA }

	all := targetsFrom(paths, "", prev)
	if len(all) != 2 || all["/srv/prod"] != "aaaaaaaaaaaa" || all["/srv/staging"] != "bbbbbbbbbbbb" {
		t.Errorf("targetsFrom(all) = %v — each moved checkout maps to its own commit, the skipped one to none", all)
	}
	if _, ok := all["/srv/idle"]; ok {
		t.Error("a checkout with no commit became a rollback target")
	}

	one := targetsFrom(paths, "/srv/staging", prev)
	if len(one) != 1 || one["/srv/staging"] != "bbbbbbbbbbbb" {
		t.Errorf("targetsFrom(--path staging) = %v, want only staging", one)
	}
}

// TestShortSHA checks the human rendering of a commit, including the short
// inputs that must not slice out of range.
func TestShortSHA(t *testing.T) {
	if got := shortSHA("0123456789abcdef"); got != "0123456" {
		t.Errorf("shortSHA = %q, want 0123456", got)
	}
	if got := shortSHA("abc"); got != "abc" {
		t.Errorf("shortSHA = %q, want abc", got)
	}
	if got := shortSHA(""); got != "" {
		t.Errorf("shortSHA = %q, want empty", got)
	}
}

func TestRollbackTargetsErrorsWhenNeverDeployed(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	mustRepo(t, st, "o/r")

	if _, err := rollbackTargets(ctx, st, "o/r", ""); err == nil {
		t.Fatal("rollbackTargets found a target for a repo that was never deployed")
	}
}

// Each moved checkout maps to its own previous commit — the point of the per-path
// model. A manual deploy moves every checkout on its own branch, so two checkouts
// legitimately roll back to different commits, and neither is dragged onto the
// other's.
func TestRollbackTargetsMapsEachCheckoutToItsOwnCommit(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	id := mustRepo(t, st, "o/r")

	d := mustDeploy(t, st, id, "d1", "", store.StatusSuccess)
	mustInsert(t, st, store.DeployPath{DeployID: d, Path: "/var/www/prod", PreviousSHA: "P0", NewSHA: "P", Status: store.StatusSuccess})
	mustInsert(t, st, store.DeployPath{DeployID: d, Path: "/var/www/staging", PreviousSHA: "S0", NewSHA: "S", Status: store.StatusSuccess})

	got, err := rollbackTargets(ctx, st, "o/r", "")
	if err != nil {
		t.Fatalf("rollbackTargets: %v", err)
	}
	if got["/var/www/prod"] != "P0" || got["/var/www/staging"] != "S0" {
		t.Errorf("targets = %v, want each checkout at its own previous commit", got)
	}
}

// The scenario round 2 reported: a push to develop moved staging and skipped
// prod, so prod's row carries no commit. The rollback must target staging alone
// and leave prod out entirely — not refuse, and above all not reset prod onto
// staging's develop commit.
func TestRollbackTargetsLeavesOutACheckoutTheDeploySkipped(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	id := mustRepo(t, st, "o/r")

	// An earlier push to main moved prod.
	m := mustDeploy(t, st, id, "d1", "refs/heads/main", store.StatusSuccess)
	mustInsert(t, st, store.DeployPath{DeployID: m, Path: "/var/www/prod", PreviousSHA: "P0", NewSHA: "P", Status: store.StatusSuccess})
	mustInsert(t, st, store.DeployPath{DeployID: m, Path: "/var/www/staging", Status: store.StatusSkipped})

	// The last push, to develop: staging moved, prod skipped (empty SHAs).
	dv := mustDeploy(t, st, id, "d2", "refs/heads/develop", store.StatusSuccess)
	mustInsert(t, st, store.DeployPath{DeployID: dv, Path: "/var/www/staging", PreviousSHA: "Q0", NewSHA: "Q", Status: store.StatusSuccess})
	mustInsert(t, st, store.DeployPath{DeployID: dv, Path: "/var/www/prod", Status: store.StatusSkipped})

	got, err := rollbackTargets(ctx, st, "o/r", "")
	if err != nil {
		t.Fatalf("rollbackTargets: %v", err)
	}
	if len(got) != 1 || got["/var/www/staging"] != "Q0" {
		t.Errorf("targets = %v, want only staging at Q0 — prod was not moved and must not be a target", got)
	}
	if _, ok := got["/var/www/prod"]; ok {
		t.Error("prod is a rollback target — it would be hard-reset onto staging's develop commit")
	}
}

func TestRollbackTargetsPathFilter(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	id := mustRepo(t, st, "o/r")

	d := mustDeploy(t, st, id, "d1", "refs/heads/develop", store.StatusSuccess)
	mustInsert(t, st, store.DeployPath{DeployID: d, Path: "/var/www/staging", PreviousSHA: "Q0", NewSHA: "Q", Status: store.StatusSuccess})
	mustInsert(t, st, store.DeployPath{DeployID: d, Path: "/var/www/prod", Status: store.StatusSkipped})

	got, err := rollbackTargets(ctx, st, "o/r", "/var/www/staging")
	if err != nil {
		t.Fatalf("rollbackTargets(--path staging): %v", err)
	}
	if len(got) != 1 || got["/var/www/staging"] != "Q0" {
		t.Errorf("targets = %v, want only staging", got)
	}

	// --path names a checkout the deploy did not move: an error, not a silent no-op.
	if _, err := rollbackTargets(ctx, st, "o/r", "/var/www/prod"); err == nil {
		t.Error("rollbackTargets invented a target for a checkout the last deploy skipped")
	}
}

// A hard-killed deploy records no rows, and rollback must still recover: each
// checkout returns to where it was last recorded, its prior new_sha.
func TestRollbackTargetsFallsBackWhenTheDeployWasKilled(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	id := mustRepo(t, st, "o/r")

	done := mustDeploy(t, st, id, "d1", "refs/heads/main", store.StatusSuccess)
	mustInsert(t, st, store.DeployPath{DeployID: done, Path: "/var/www/prod", PreviousSHA: "old", NewSHA: "live", Status: store.StatusSuccess})

	// A second deploy, hard-killed before it recorded anything.
	mustDeploy(t, st, id, "d2", "refs/heads/main", store.StatusRunning)

	got, err := rollbackTargets(ctx, st, "o/r", "")
	if err != nil {
		t.Fatalf("rollbackTargets: %v — rollback is dead after a killed deploy", err)
	}
	// "live" is where the tree stood when the killed deploy moved it — not "old".
	if got["/var/www/prod"] != "live" {
		t.Errorf("target = %q, want live", got["/var/www/prod"])
	}
}

// A deploy that merely failed recorded nothing and never moved a tree, so there
// is nothing to undo. It must error, not "roll back" a tree to where it already
// sits and call that a success.
func TestRollbackTargetsDoesNotFallBackForAFailedDeploy(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	id := mustRepo(t, st, "o/r")

	done := mustDeploy(t, st, id, "d1", "refs/heads/main", store.StatusSuccess)
	mustInsert(t, st, store.DeployPath{DeployID: done, Path: "/var/www/r", PreviousSHA: "old", NewSHA: "live", Status: store.StatusSuccess})

	failed := mustDeploy(t, st, id, "d2", "refs/heads/main", store.StatusRunning)
	if err := st.DeployFinish(ctx, failed, store.StatusFailed); err != nil {
		t.Fatalf("DeployFinish: %v", err)
	}

	if _, err := rollbackTargets(ctx, st, "o/r", ""); err == nil {
		t.Error("rollbackTargets found a target for a deploy that never moved a tree")
	}
}

// describeRollback lists each checkout and its target, so the confirm prompt
// shows which trees move and onto what.
func TestDescribeRollback(t *testing.T) {
	got := describeRollback(map[string]string{"/var/www/prod": "abcdef1234", "/var/www/staging": "0987654321"})

	if !strings.Contains(got, "/var/www/prod → abcdef1") || !strings.Contains(got, "/var/www/staging → 0987654") {
		t.Errorf("describeRollback = %q, want each path and its short sha", got)
	}
	// Sorted, so prod comes before staging.
	if strings.Index(got, "prod") > strings.Index(got, "staging") {
		t.Errorf("describeRollback = %q, want paths sorted", got)
	}
}

// --- helpers ---

func openTestStore(t *testing.T) *store.Store {
	t.Helper()

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	return st
}

func mustRepo(t *testing.T, st *store.Store, slug string) int64 {
	t.Helper()

	id, err := st.RepoInsert(context.Background(), store.Repo{Repository: slug, Token: "tok-" + slug, Secret: "sec"})
	if err != nil {
		t.Fatalf("RepoInsert %s: %v", slug, err)
	}

	return id
}

func mustDeploy(t *testing.T, st *store.Store, repoID int64, delivery, ref, status string) int64 {
	t.Helper()

	id, err := st.DeployStart(context.Background(), store.Deploy{RepoID: repoID, DeliveryID: delivery, Ref: ref, SHA: "x", Status: status})
	if err != nil {
		t.Fatalf("DeployStart: %v", err)
	}

	return id
}

func mustInsert(t *testing.T, st *store.Store, p store.DeployPath) {
	t.Helper()

	if err := st.DeployPathInsert(context.Background(), p); err != nil {
		t.Fatalf("DeployPathInsert %s: %v", p.Path, err)
	}
}

// TestResultFlagsKeepsAFailureReasonOffTheSummaryLine pins the split renderResult
// depends on. A failure's reason now carries the failing command's own output —
// up to privexec.ExcerptRunes characters — and gluing that onto the flags list
// would smear the one-line-per-checkout layout. A skip's reason is a short
// phrase and belongs inline.
func TestResultFlagsKeepsAFailureReasonOffTheSummaryLine(t *testing.T) {
	reason := "command failed with exit 128: git fetch --all --prune: git@github.com: Permission denied (publickey)."

	failed := strings.Join(resultFlags(deploy.PathResult{
		Path: "/srv/site", User: "admin", Status: store.StatusFailed, Reason: reason,
	}), "  ")
	if strings.Contains(failed, reason) {
		t.Errorf("resultFlags of a failure = %q, want the reason rendered on its own line instead", failed)
	}
	if !strings.Contains(failed, store.StatusFailed) {
		t.Errorf("resultFlags of a failure = %q, want the status", failed)
	}

	skipped := strings.Join(resultFlags(deploy.PathResult{
		Path: "/srv/site", User: "admin", Status: store.StatusSkipped, Reason: "checkout is on develop, push was to main",
	}), "  ")
	if !strings.Contains(skipped, "checkout is on develop") {
		t.Errorf("resultFlags of a skip = %q, want the reason inline", skipped)
	}
}

func TestDeployCmdHasASetupFlag(t *testing.T) {
	cmd := newDeployCmd()

	f := cmd.Flags().Lookup("setup")
	if f == nil {
		t.Fatal("--setup is not registered")
	}
	if f.DefValue != "false" {
		t.Errorf("--setup default = %q, want false", f.DefValue)
	}
	if !strings.Contains(f.Usage, "setup") {
		t.Errorf("--setup usage = %q", f.Usage)
	}
}

// TestRollbackRecoversNamesTheRunsThatNeedRecovery is the hint the report owes.
// A path that failed after its tree had already moved is left on the new commit
// — deliberately un-rolled-back on a setup run — and `rec-deploy logs` alone
// tells the operator what broke, not how to get the site back. Its row carries a
// usable rollback target, so `repo rollback` recovers it; nothing said so.
func TestRollbackRecoversNamesTheRunsThatNeedRecovery(t *testing.T) {
	moved := deploy.Result{Paths: []deploy.PathResult{
		{Path: "/srv/a", Status: store.StatusFailed, PreviousSHA: "aaaaaaa", NewSHA: "bbbbbbb"},
	}}
	if !rollbackRecovers(moved, true) {
		t.Error("a failure that left the tree on a new commit offers no recovery hint")
	}

	// A failure before the sync — the pre-sync setup guard, a lock it could not
	// take — left nothing to undo, and pointing at rollback there would send the
	// operator to reset a tree this run never touched.
	untouched := deploy.Result{Paths: []deploy.PathResult{
		{Path: "/srv/a", Status: store.StatusFailed, PreviousSHA: "aaaaaaa"},
	}}
	if rollbackRecovers(untouched, true) {
		t.Error("a failure that never moved the tree offers a rollback that would undo nothing")
	}

	// A rolled-back path is already back on its previous commit.
	rolled := deploy.Result{Paths: []deploy.PathResult{
		{Path: "/srv/a", Status: store.StatusRolledBack, PreviousSHA: "aaaaaaa", NewSHA: "aaaaaaa"},
	}}
	if rollbackRecovers(rolled, true) {
		t.Error("a rolled-back path still asks to be rolled back")
	}

	// A success moved its tree on purpose.
	ok := deploy.Result{Paths: []deploy.PathResult{
		{Path: "/srv/a", Status: store.StatusSuccess, PreviousSHA: "aaaaaaa", NewSHA: "bbbbbbb"},
	}}
	if rollbackRecovers(ok, true) {
		t.Error("a successful deploy is offered a rollback")
	}
}

// TestRollbackRecoversNeverPointsARollbackAtItself is the trap the hint set for
// itself. renderResult is shared by deploy and rollback, and a rollback moves
// trees too: rollbackTo records new_sha as the commit it restored and
// previous_sha as the bad one it escaped. So a rollback whose re-run pipeline
// failed looks exactly like a deploy that failed after moving — and following
// the hint would call rollbackTargets again, read that row's previous_sha, and
// reset every checkout forward onto the very commit the operator was undoing.
func TestRollbackRecoversNeverPointsARollbackAtItself(t *testing.T) {
	// A rollback that reset the tree and then failed re-running the pipeline the
	// restored commit carries.
	res := deploy.Result{Paths: []deploy.PathResult{
		{Path: "/srv/a", Status: store.StatusFailed, PreviousSHA: "bbbbbbb", NewSHA: "aaaaaaa"},
	}}

	if rollbackRecovers(res, false) {
		t.Error("a failed rollback offers another rollback, which would push the checkout back onto the commit it just escaped")
	}
}
