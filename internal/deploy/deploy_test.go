package deploy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/discover"
	"github.com/rdcstarr/rec-deploy/internal/privexec"
)

// git runs a git command in dir, failing the test on error.
//
// The global and system config are cut out deliberately. They are not noise: a
// developer's ~/.gitconfig routinely carries `url.git@github.com:.insteadOf =
// https://github.com/`, and `git remote get-url` expands insteadOf, so the
// origin assertions below would read back a rewritten URL and pass no matter
// what the code under test did. Cutting the config out also keeps commit.gpgsign,
// core.hooksPath and init.defaultBranch from reaching these repositories. The
// identity comes from the environment, since there is no global config to supply
// one.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}

	return strings.TrimSpace(string(out))
}

// origin creates a bare repo with one commit on main carrying the given
// manifest, and returns its path.
func origin(t *testing.T, manifestBody string) string {
	t.Helper()

	bare := filepath.Join(t.TempDir(), "origin.git")
	git(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)

	work := t.TempDir()
	git(t, work, "clone", bare, work)
	if err := os.WriteFile(filepath.Join(work, ".rec-deploy.yml"), []byte(manifestBody), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "initial")
	git(t, work, "push", "origin", "main")

	return bare
}

// clone makes a scratch working copy of bare, used only to push new commits.
func clone(t *testing.T, bare string) string {
	t.Helper()

	dir := t.TempDir()
	git(t, dir, "clone", bare, dir)

	return dir
}

// checkout builds the deploy target: a working copy whose origin is the GitHub
// SSH URL the manifest declares — that is what discover parses out of
// .git/config, and a plain `git clone <local path>` would leave an origin no
// GitHub URL parser accepts. git still reaches the local bare repo, through an
// insteadOf rewrite, so the test needs no network, no deploy key and no agent.
//
// The checkout lives alone under its own parent, which is the discovery root.
func checkout(t *testing.T, bare, repository string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("create checkout dir: %v", err)
	}

	url := "git@github.com:" + repository + ".git"

	git(t, dir, "init", "--initial-branch=main", dir)
	git(t, dir, "remote", "add", "origin", url)
	git(t, dir, "config", "url."+bare+".insteadOf", url)
	git(t, dir, "fetch", "origin")
	git(t, dir, "reset", "--hard", "origin/main")

	return dir
}

// opts wires a Run against a single checkout. The keys dir is empty, so no key
// is found and no agent is started — exactly the public-repo path.
func opts(t *testing.T, dir, repository, ref string) Options {
	t.Helper()

	return Options{
		Repository: repository,
		Ref:        ref,
		Path:       dir,
		Roots:      []string{filepath.Dir(dir)},
		LocksDir:   t.TempDir(),
		KeysDir:    t.TempDir(),
	}
}

func TestRunRunsThePipeline(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - echo deployed > marker\n")
	dir := checkout(t, bare, "o/r")

	res, err := Run(context.Background(), opts(t, dir, "o/r", "refs/heads/main"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("Status = %s, want success: %+v", res.Status, res.Paths)
	}
	if len(res.Paths) != 1 || len(res.Paths[0].Commands) != 1 {
		t.Fatalf("Paths = %+v", res.Paths)
	}
	if res.Paths[0].PreviousSHA == "" || res.Paths[0].NewSHA == "" {
		t.Errorf("Paths[0] = %+v, want both shas recorded", res.Paths[0])
	}

	if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
		t.Errorf("the post_deploy step did not run: %v", err)
	}
}

// A valid manifest with no post_deploy steps is an intention — "pull the code,
// run nothing" — not the silent empty pipeline an old implementation reports as success.
func TestRunAcceptsAManifestWithNoSteps(t *testing.T) {
	bare := origin(t, "repository: o/r\n")
	dir := checkout(t, bare, "o/r")

	res, err := Run(context.Background(), opts(t, dir, "o/r", "refs/heads/main"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Errorf("Status = %s, want success", res.Status)
	}
}

// Every path deploys under the advisory lock: while another deploy holds it,
// this one waits and then gives up rather than running a second concurrent
// `git reset --hard` on the same working tree.
func TestRunWaitsForTheLockOnThePath(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - echo deployed > marker\n")
	dir := checkout(t, bare, "o/r")

	o := opts(t, dir, "o/r", "refs/heads/main")

	release, err := Lock(context.Background(), o.LocksDir, dir)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	res, err := Run(ctx, o)
	if err == nil {
		t.Fatal("Run against a locked path returned nil error")
	}
	if len(res.Paths) != 1 || res.Paths[0].Status != "failed" ||
		!strings.Contains(res.Paths[0].Reason, "lock") {
		t.Fatalf("Paths = %+v, want one failure naming the lock", res.Paths)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
		t.Error("the pipeline ran on a tree another deploy holds locked")
	}
}

// A push to feature/x must not re-run the whole post_deploy on a checkout that
// is on main. an old implementation deploys whatever branch happens to be checked out.
func TestRunSkipsAPathOnAnotherBranch(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - echo deployed > marker\n")
	dir := checkout(t, bare, "o/r")

	res, err := Run(context.Background(), opts(t, dir, "o/r", "refs/heads/feature/x"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "skipped" {
		t.Errorf("Status = %s, want skipped", res.Status)
	}
	if len(res.Paths) != 1 || res.Paths[0].Status != "skipped" || res.Paths[0].Reason == "" {
		t.Fatalf("Paths = %+v, want one skip with a reason", res.Paths)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
		t.Error("the pipeline ran on a branch that was not pushed")
	}
}

// A manual deploy passes no ref and deploys the branch each checkout is on.
func TestRunWithoutARefDeploysTheCheckoutsOwnBranch(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - echo deployed > marker\n")
	dir := checkout(t, bare, "o/r")

	res, err := Run(context.Background(), opts(t, dir, "o/r", ""))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("Status = %s, want success: %+v", res.Status, res.Paths)
	}
}

// Zero installations is an error, reported. the old implementation's whenNotEmpty skips
// the notification chain entirely, so a misnamed marker file is silent.
func TestRunZeroInstallationsIsAnError(t *testing.T) {
	o := Options{
		Repository: "o/absent",
		Ref:        "refs/heads/main",
		Roots:      []string{t.TempDir()},
		LocksDir:   t.TempDir(),
		KeysDir:    t.TempDir(),
	}

	res, err := Run(context.Background(), o)
	if err == nil {
		t.Fatal("Run with no installations returned nil error")
	}
	if res.Status != "failed" {
		t.Errorf("Status = %s, want failed", res.Status)
	}
}

func TestRunRollsBackOnFailure(t *testing.T) {
	bare := origin(t, "repository: o/r\nrollback_on_failure: true\npost_deploy:\n  - echo v1 > marker\n")
	dir := checkout(t, bare, "o/r")

	before := git(t, dir, "rev-parse", "HEAD")

	// Push a second commit whose pipeline fails.
	work := clone(t, bare)
	if err := os.WriteFile(filepath.Join(work, ".rec-deploy.yml"),
		[]byte("repository: o/r\nrollback_on_failure: true\npost_deploy:\n  - exit 1\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	git(t, work, "commit", "-am", "break it")
	git(t, work, "push", "origin", "main")

	res, err := Run(context.Background(), opts(t, dir, "o/r", "refs/heads/main"))
	if err == nil {
		t.Fatal("Run of a failing pipeline returned nil error")
	}
	if res.Status != "failed" {
		t.Errorf("Status = %s, want failed", res.Status)
	}
	if len(res.Paths) != 1 || res.Paths[0].Status != "rolled_back" {
		t.Fatalf("Paths = %+v, want rolled_back", res.Paths)
	}

	if after := git(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD = %s, want the pre-deploy sha %s — the rollback did not reset the tree", after, before)
	}
	// The rollback re-ran the previous manifest, which recreates the marker.
	body, err := os.ReadFile(filepath.Join(dir, "marker"))
	if err != nil || strings.TrimSpace(string(body)) != "v1" {
		t.Errorf("marker = %q, %v — the previous manifest's post_deploy did not re-run", body, err)
	}
}

func TestRunWithoutRollbackLeavesTheTreeOnTheNewSHA(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - exit 1\n")
	dir := checkout(t, bare, "o/r")

	res, err := Run(context.Background(), opts(t, dir, "o/r", "refs/heads/main"))
	if err == nil {
		t.Fatal("want an error")
	}
	if len(res.Paths) != 1 || res.Paths[0].Status != "failed" {
		t.Fatalf("Paths = %+v, want failed (not rolled_back — rollback_on_failure is off)", res.Paths)
	}
}

// The timeout is real and it kills the process. This is the an old implementation defect
// that silently kills a cold `composer install` at 60s.
func TestRunStepTimeoutKillsTheProcess(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - run: sleep 30\n    timeout: 300ms\n")
	dir := checkout(t, bare, "o/r")

	start := time.Now()
	res, err := Run(context.Background(), opts(t, dir, "o/r", "refs/heads/main"))
	if err == nil {
		t.Fatal("a timed-out step returned nil error")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("Run took %s — the step was not killed", elapsed)
	}
	if len(res.Paths) != 1 || len(res.Paths[0].Commands) != 1 || !res.Paths[0].Commands[0].TimedOut {
		t.Errorf("Paths = %+v, want a TimedOut command", res.Paths)
	}
}

// A missing manifest in the pulled tree is a hard failure, never a success with
// an empty pipeline.
func TestRunFailsOnAMissingManifest(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - true\n")
	dir := checkout(t, bare, "o/r")

	// Remove the manifest upstream, so the fresh tree has none.
	work := clone(t, bare)
	git(t, work, "rm", ".rec-deploy.yml")
	git(t, work, "commit", "-m", "drop the manifest")
	git(t, work, "push", "origin", "main")

	res, err := Run(context.Background(), opts(t, dir, "o/r", "refs/heads/main"))
	if err == nil {
		t.Fatal("a deploy whose fresh tree has no manifest returned nil error")
	}
	if res.Status != "failed" {
		t.Errorf("Status = %s, want failed", res.Status)
	}
	if len(res.Paths) != 1 || res.Paths[0].Status != "failed" {
		t.Fatalf("Paths = %+v, want one failed path", res.Paths)
	}
}

// The deploy key only authenticates over SSH, so an https origin is rewritten
// before the sync — otherwise git ignores the key and a private repo never pulls.
func TestUseSSHRemoteRewritesAnHTTPSOrigin(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "--initial-branch=main", dir)
	git(t, dir, "remote", "add", "origin", "https://github.com/rdcstarr/tema.git")

	if err := useSSHRemote(context.Background(), "rdcstarr/tema", privexec.Options{Dir: dir}); err != nil {
		t.Fatalf("useSSHRemote: %v", err)
	}

	if got := git(t, dir, "remote", "get-url", "origin"); got != "git@github.com:rdcstarr/tema.git" {
		t.Errorf("origin = %q, want the ssh form", got)
	}
}

func TestRunContinueOnFailure(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - run: exit 1\n    continue_on_failure: true\n  - echo reached > marker\n")
	dir := checkout(t, bare, "o/r")

	res, err := Run(context.Background(), opts(t, dir, "o/r", "refs/heads/main"))
	if err != nil {
		t.Fatalf("Run: %v — continue_on_failure must not fail the deploy", err)
	}
	if res.Status != "success" {
		t.Errorf("Status = %s, want success", res.Status)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
		t.Errorf("the pipeline stopped at the failing step: %v", err)
	}
}

// Rollback resets the tree to the commit the caller read out of the last
// deploy's previous_sha, and re-runs the manifest that reset tree carries.
func TestRollbackResetsToTheGivenSHA(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - echo v1 > marker\n")
	dir := checkout(t, bare, "o/r")

	before := git(t, dir, "rev-parse", "HEAD")

	// A second commit, deployed normally.
	work := clone(t, bare)
	if err := os.WriteFile(filepath.Join(work, ".rec-deploy.yml"),
		[]byte("repository: o/r\npost_deploy:\n  - echo v2 > marker\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	git(t, work, "commit", "-am", "v2")
	git(t, work, "push", "origin", "main")

	if _, err := Run(context.Background(), opts(t, dir, "o/r", "refs/heads/main")); err != nil {
		t.Fatalf("Run: %v", err)
	}

	o := opts(t, dir, "o/r", "")
	o.RollbackSHAs = map[string]string{dir: before}

	res, err := Rollback(context.Background(), o)
	if err != nil {
		t.Fatalf("Rollback: %v — %+v", err, res.Paths)
	}
	if res.Status != "success" {
		t.Errorf("Status = %s, want success", res.Status)
	}

	if after := git(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD = %s, want %s", after, before)
	}
	// The reset tree carries the previous manifest, and that is what re-runs.
	body, err := os.ReadFile(filepath.Join(dir, "marker"))
	if err != nil || strings.TrimSpace(string(body)) != "v1" {
		t.Errorf("marker = %q, %v — the reset tree's post_deploy did not re-run", body, err)
	}
}

// Rollback resets only the checkouts it has a target for, never one it merely
// discovered. A checkout added to the server since the last deploy — or one that
// deploy skipped — has no entry in the map, and the residual bug was that it got
// reset anyway, onto a commit belonging to a sibling. It must be left untouched.
func TestRollbackLeavesADiscoveredCheckoutWithNoTargetUntouched(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - true\n")
	prod := checkout(t, bare, "o/r")

	// A second checkout of the same repo, on the same branch, deployed once so it
	// sits at a real commit. It is discovered by the roots glob alongside prod.
	staging := checkout(t, bare, "o/r")

	prodStart := git(t, prod, "rev-parse", "HEAD")
	stagingStart := git(t, staging, "rev-parse", "HEAD")

	// A new commit, and prod deployed onto it — so prod has somewhere to roll back
	// to, while staging stays where it is.
	work := clone(t, bare)
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "v2")
	git(t, work, "push", "origin", "main")
	if _, err := Run(context.Background(), opts(t, prod, "o/r", "refs/heads/main")); err != nil {
		t.Fatalf("Run: %v", err)
	}

	o := opts(t, prod, "o/r", "")
	o.Path = "" // roll back the repository, not one checkout
	// Each checkout lives alone under its own parent, so both parents are roots.
	o.Roots = []string{filepath.Dir(prod), filepath.Dir(staging)}
	// Only prod has a target; staging is discoverable but absent from the map.
	o.RollbackSHAs = map[string]string{prod: prodStart}

	res, err := Rollback(context.Background(), o)
	if err != nil {
		t.Fatalf("Rollback: %v — %+v", err, res.Paths)
	}

	if after := git(t, prod, "rev-parse", "HEAD"); after != prodStart {
		t.Errorf("prod HEAD = %s, want it rolled back to %s", after, prodStart)
	}
	if after := git(t, staging, "rev-parse", "HEAD"); after != stagingStart {
		t.Errorf("staging HEAD = %s, want it untouched at %s — it was reset with no recorded target", after, stagingStart)
	}
	// staging appears in the result as skipped, not silently dropped.
	var sawStagingSkipped bool
	for _, pr := range res.Paths {
		if pr.Path == staging {
			sawStagingSkipped = pr.Status == "skipped"
		}
	}
	if !sawStagingSkipped {
		t.Errorf("staging was not reported as skipped: %+v", res.Paths)
	}
}

// Without any target there is nothing to reset to: the engine keeps no history,
// the caller resolves the per-checkout targets from the store.
func TestRollbackWithNoTargetsIsAnError(t *testing.T) {
	if _, err := Rollback(context.Background(), Options{Repository: "o/r", Roots: []string{t.TempDir()}}); err == nil {
		t.Fatal("Rollback with no targets returned nil error")
	}
}

// The deploy key is saved under the registered slug and sshkey.Path is a
// case-sensitive filename, while a checkout's origin can differ from the
// registration in case: GitHub resolves slugs case-insensitively, so
// `repo add rdcstarr/tema` registers a repository whose canonical origin URL is
// RdcStarr/Tema. Keying the lookup on the checkout's origin turns that into a
// miss, and a miss is indistinguishable from "no key on this server" — the
// deploy proceeds unauthenticated, with no agent and no pinned known_hosts, and
// fails on every push to a private repository.
//
// An unusable key under the registered slug makes the lookup observable: looked
// up there, StartAgent rejects it and the deploy fails; looked up under the
// origin's casing, it is missed and the deploy sails past.
func TestRunLoadsTheKeyUnderTheRegisteredSlugNotTheOriginCasing(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - echo deployed > marker\n")
	dir := checkout(t, bare, "O/R") // origin carries GitHub's canonical casing

	o := opts(t, dir, "o/r", "refs/heads/main") // the registration is lowercase
	for name, body := range map[string]string{"o_r": "not a key", "o_r.pub": "not a key\n"} {
		if err := os.WriteFile(filepath.Join(o.KeysDir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("Run succeeded — the key registered for o/r was never looked up, so the deploy ran unauthenticated")
	}
}

// A checkout with an SSH origin and no key on this server still deploys — public
// repositories need no key — but its host key must still be pinned. Without
// GIT_SSH_COMMAND, git reads the site user's ~/.ssh/config and known_hosts
// instead: files rec-deploy does not control, and which on a shared box routinely
// carry StrictHostKeyChecking=no. That is the exact hole the pin exists to close,
// left open for every keyless checkout.
func TestPrepareKeylessSSHCheckoutStillPinsTheHostKey(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - true\n")
	dir := checkout(t, bare, "o/r")

	o := opts(t, dir, "o/r", "refs/heads/main")
	o.KnownHosts = filepath.Join(t.TempDir(), "known_hosts")

	exec, cleanup, err := prepare(context.Background(), discover.Installation{
		Path: dir, UID: os.Getuid(), GID: os.Getgid(),
	}, o)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer cleanup()

	var cmd string
	for _, e := range exec.Env {
		if v, ok := strings.CutPrefix(e, "GIT_SSH_COMMAND="); ok {
			cmd = v
		}
	}
	if cmd == "" {
		t.Fatal("a keyless ssh checkout got no GIT_SSH_COMMAND — git would fall back to the site user's ssh config and known_hosts")
	}
	if !strings.Contains(cmd, "UserKnownHostsFile="+o.KnownHosts) || !strings.Contains(cmd, "StrictHostKeyChecking=yes") {
		t.Errorf("GIT_SSH_COMMAND = %q, host key not pinned", cmd)
	}
}

// An HTTPS checkout with no key must keep its origin. The rewrite to SSH exists
// so a deploy key can authenticate — with no key there is nothing to authenticate
// with, and rewriting turns a public checkout that works into an SSH fetch that
// can never succeed.
func TestPrepareLeavesAKeylessHTTPSCheckoutOnHTTPS(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - true\n")
	dir := checkout(t, bare, "o/r")

	// An HTTPS origin, as `git clone https://github.com/o/r.git` leaves it. Read
	// back with `config --get`, not `remote get-url`: get-url expands the
	// insteadOf this checkout uses to reach its local bare origin, which maps the
	// rewritten SSH URL straight back onto the same value and hides the rewrite.
	const httpsURL = "https://github.com/o/r.git"
	git(t, dir, "remote", "set-url", "origin", httpsURL)

	_, cleanup, err := prepare(context.Background(), discover.Installation{
		Path: dir, UID: os.Getuid(), GID: os.Getgid(), RemoteHTTPS: true,
	}, opts(t, dir, "o/r", "refs/heads/main"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer cleanup()

	if after := strings.TrimSpace(git(t, dir, "config", "--get", "remote.origin.url")); after != httpsURL {
		t.Errorf("origin rewritten to %q with no key to authenticate with — it must stay %q", after, httpsURL)
	}
}

// The rollback is the safety net for a deploy that did not finish, so it must
// survive the very cancellation that ended it — Ctrl+C, or a drain past its
// budget. Run it on the deploy's own context and privexec cannot fork a process
// at all, so the net is dead in one of the cases that most needs it: the tree is
// left on the new SHA with a pipeline that ran partway.
func TestRunRollsBackAfterTheDeployIsCancelled(t *testing.T) {
	bare := origin(t, "repository: o/r\nrollback_on_failure: true\npost_deploy:\n  - echo v1 > marker\n")
	dir := checkout(t, bare, "o/r")

	before := git(t, dir, "rev-parse", "HEAD")

	// A second commit whose pipeline blocks long enough to be cancelled mid-step.
	work := clone(t, bare)
	if err := os.WriteFile(filepath.Join(work, ".rec-deploy.yml"),
		[]byte("repository: o/r\nrollback_on_failure: true\npost_deploy:\n  - sleep 30\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	git(t, work, "commit", "-am", "slow")
	git(t, work, "push", "origin", "main")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(700 * time.Millisecond)
		cancel()
	}()

	res, err := Run(ctx, opts(t, dir, "o/r", "refs/heads/main"))
	if err == nil {
		t.Fatal("Run of a cancelled deploy returned nil error")
	}
	if len(res.Paths) != 1 || res.Paths[0].Status != "rolled_back" {
		t.Fatalf("Paths = %+v, want rolled_back — the rollback could not run on the cancelled context", res.Paths)
	}
	if after := git(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD = %s, want the pre-deploy sha %s — the tree was left half-deployed", after, before)
	}
}

// The killed-deploy fallback maps every recorded checkout to its own last commit,
// so a checkout the kill never moved is mapped to where it already sits. A reset
// there is a no-op, but re-running its pipeline is not — it fires post_deploy on a
// tree the operator never targeted. A checkout already at its target is skipped,
// pipeline and all.
func TestRollbackSkipsACheckoutAlreadyAtItsTarget(t *testing.T) {
	bare := origin(t, "repository: o/r\npost_deploy:\n  - touch "+filepath.Join(t.TempDir(), "unused")+"\n")
	dir := checkout(t, bare, "o/r")

	// A committed manifest whose pipeline leaves a visible mark.
	marker := filepath.Join(dir, "reran")
	work := clone(t, bare)
	if err := os.WriteFile(filepath.Join(work, ".rec-deploy.yml"), []byte("repository: o/r\npost_deploy:\n  - touch "+marker+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	git(t, work, "commit", "-am", "mark")
	git(t, work, "push", "origin", "main")
	git(t, dir, "fetch", "origin")
	git(t, dir, "reset", "--hard", "origin/main")
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clear marker: %v", err)
	}

	head := git(t, dir, "rev-parse", "HEAD")
	o := opts(t, dir, "o/r", "")
	o.RollbackSHAs = map[string]string{dir: head} // target == current HEAD

	res, err := Rollback(context.Background(), o)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(res.Paths) != 1 || res.Paths[0].Status != "skipped" {
		t.Fatalf("Paths = %+v, want the checkout skipped", res.Paths)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("post_deploy re-ran on a checkout already at its rollback target")
	}
}
