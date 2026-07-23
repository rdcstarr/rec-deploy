// Package deploy is the engine: it locks a working tree, filters on the pushed
// branch, syncs it with git as the directory's owner, runs the manifest's
// post_deploy pipeline under real timeouts, and rolls back on failure.
//
// Every defect the rewrite exists to fix lives here: the lock that an old implementation
// does not take, the branch filter it does not apply, the timeout it parses and
// ignores, the missing manifest it reports as a success, and the zero
// installations it passes over in silence.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/discover"
	"github.com/rdcstarr/rec-deploy/internal/manifest"
	"github.com/rdcstarr/rec-deploy/internal/privexec"
	"github.com/rdcstarr/rec-deploy/internal/sshkey"
	"github.com/rdcstarr/rec-deploy/internal/store"
)

// rollbackTimeout bounds a rollback, which runs detached from the deploy that
// triggered it. It re-runs the previous manifest's post_deploy, so it is budgeted
// like a deploy rather than like a single command.
const rollbackTimeout = 2 * time.Hour

// Options configures one deploy of one repository across every installation.
type Options struct {
	// Repository is the owner/repo slug being deployed.
	Repository string
	// Ref is the pushed ref (refs/heads/main). Empty means a manual deploy:
	// every installation deploys the branch it is on.
	Ref string
	// SHA, Message and Author come from the push and are recorded with the
	// result.
	SHA, Message, Author string
	// RollbackSHAs is the target commit for each checkout a Rollback must reset,
	// keyed by the checkout's absolute path. A checkout with no entry is left
	// where it is: only the checkouts the last deploy moved appear here, each
	// mapped to its own previous commit, so no tree is ever reset onto a commit
	// resolved from a sibling on another branch.
	RollbackSHAs map[string]string
	// Path restricts the deploy to one installation.
	Path string
	// Roots and Prune configure discovery.
	Roots, Prune []string
	// KeysDir, LocksDir and KnownHosts are rec-deploy's state paths.
	KeysDir, LocksDir, KnownHosts string
	// Stream, when non-nil, receives command output live. `rec-deploy deploy`
	// streams; it must never spin on a dead pause.
	Stream io.Writer
}

// CommandResult is one post_deploy step's outcome.
type CommandResult struct {
	Command  string        `json:"command"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
	Output   string        `json:"output"`
	TimedOut bool          `json:"timed_out"`
}

// PathResult is one installation's outcome.
type PathResult struct {
	Path string `json:"path"`
	User string `json:"user"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	// RanAsRoot marks a root-owned target. Allowed, but flagged everywhere it
	// surfaces: push access to such a repository is root on this server.
	RanAsRoot   bool            `json:"ran_as_root"`
	PreviousSHA string          `json:"previous_sha"`
	NewSHA      string          `json:"new_sha"`
	Status      string          `json:"status"`
	Reason      string          `json:"reason,omitempty"`
	Commands    []CommandResult `json:"commands,omitempty"`
}

// Result is the whole deploy, fanned out over every installation.
type Result struct {
	Repository string       `json:"repository"`
	Ref        string       `json:"ref,omitempty"`
	SHA        string       `json:"sha,omitempty"`
	Message    string       `json:"message,omitempty"`
	Author     string       `json:"author,omitempty"`
	Status     string       `json:"status"`
	Paths      []PathResult `json:"paths"`
}

// Run deploys every installation of opts.Repository. Zero installations is an
// error: a repository rec-deploy administers but cannot find on disk is a
// misconfiguration, and the old implementation's silence about it is the defect.
func Run(ctx context.Context, opts Options) (Result, error) {
	res := newResult(opts)

	targets, err := targets(ctx, opts)
	if err != nil {
		res.Status = store.StatusFailed
		return res, err
	}

	for _, in := range targets {
		res.Paths = append(res.Paths, deployPath(ctx, in, opts))
	}

	summarize(&res)
	if res.Status == store.StatusFailed {
		return res, fmt.Errorf("deploy of %s failed on at least one path", opts.Repository)
	}

	return res, nil
}

// Rollback resets each checkout named in opts.RollbackSHAs to the commit mapped
// for it, and re-runs the manifest the reset tree carries — the manifest versions
// with the code, so the previous commit's pipeline is the right one to run.
//
// The engine keeps no history: the caller reads each checkout's previous commit
// out of the last deploy and passes the map in. A checkout the deploy did not
// move is not in the map, so it is discovered and then left untouched — never
// reset onto a commit that belongs to a sibling on another branch.
func Rollback(ctx context.Context, opts Options) (Result, error) {
	res := newResult(opts)

	if len(opts.RollbackSHAs) == 0 {
		res.Status = store.StatusFailed
		return res, fmt.Errorf("rollback needs the commit each checkout was on before the last deploy, and none were resolved for %s — `rec-deploy logs %s` shows what it recorded", opts.Repository, opts.Repository)
	}

	targets, err := targets(ctx, opts)
	if err != nil {
		res.Status = store.StatusFailed
		return res, err
	}

	for _, in := range targets {
		res.Paths = append(res.Paths, rollbackPath(ctx, in, opts))
	}

	summarize(&res)
	if res.Status == store.StatusFailed {
		return res, fmt.Errorf("rollback of %s failed on at least one path", opts.Repository)
	}

	return res, nil
}

// newResult seeds a Result with the push metadata every path result hangs off.
func newResult(opts Options) Result {
	return Result{
		Repository: opts.Repository,
		Ref:        opts.Ref,
		SHA:        opts.SHA,
		Message:    opts.Message,
		Author:     opts.Author,
	}
}

// targets finds every checkout of the repository, narrowed to opts.Path when it
// is set. Finding none is an error, never silence.
func targets(ctx context.Context, opts Options) ([]discover.Installation, error) {
	all, err := discover.Scan(ctx, discover.Options{Roots: opts.Roots, Prune: opts.Prune})
	if err != nil {
		return nil, err
	}

	found := discover.Filter(all, opts.Repository)

	if opts.Path != "" {
		var only []discover.Installation
		for _, in := range found {
			if in.Path == opts.Path {
				only = append(only, in)
			}
		}
		found = only
	}

	if len(found) == 0 {
		return nil, fmt.Errorf("no installation of %s found under the discovery roots — run `rec-deploy scan` to see what discovery finds", opts.Repository)
	}

	return found, nil
}

// summarize sets the overall status: failed if any path failed, skipped if every
// path skipped, success otherwise.
func summarize(res *Result) {
	failed, deployed := false, false

	for _, pr := range res.Paths {
		switch pr.Status {
		case store.StatusFailed, store.StatusRolledBack:
			failed = true
		case store.StatusSuccess:
			deployed = true
		}
	}

	switch {
	case failed:
		res.Status = store.StatusFailed
	case deployed:
		res.Status = store.StatusSuccess
	default:
		res.Status = store.StatusSkipped
	}
}

// deployPath deploys one installation. It never returns an error: the failure is
// the PathResult, so one broken checkout does not abort the others.
func deployPath(ctx context.Context, in discover.Installation, opts Options) PathResult {
	pr := newPathResult(in)

	if in.Err != nil {
		pr.Status, pr.Reason = store.StatusFailed, in.Err.Error()
		return pr
	}

	// The branch filter: each installation follows its own branch, so staging on
	// develop and production on main coexist on one server with no configuration
	// — and a push to feature/x never re-runs the pipeline on main.
	if branch := branchOf(opts.Ref); branch != "" && branch != in.Branch {
		pr.Status = store.StatusSkipped
		pr.Reason = fmt.Sprintf("checkout is on %s, push was to %s", in.Branch, branch)

		return pr
	}

	exec, cleanup, err := prepare(ctx, in, opts)
	if err != nil {
		pr.Status, pr.Reason = store.StatusFailed, err.Error()
		return pr
	}
	defer cleanup()

	// The rollback point, recorded before anything moves.
	if pr.PreviousSHA, err = headSHA(ctx, exec); err != nil {
		pr.Status, pr.Reason = store.StatusFailed, err.Error()
		return pr
	}

	if err := sync(ctx, in.Branch, exec); err != nil {
		pr.Status, pr.Reason = store.StatusFailed, err.Error()
		return pr
	}

	if pr.NewSHA, err = headSHA(ctx, exec); err != nil {
		pr.Status, pr.Reason = store.StatusFailed, err.Error()
		return pr
	}

	// Re-read the manifest from the freshly pulled tree: the deploy steps version
	// with the code. Missing or invalid is a hard failure — an old implementation rescues
	// the parse into an empty pipeline and reports success having run nothing.
	m, err := manifest.Load(in.Path)
	if err != nil {
		pr.Status, pr.Reason = store.StatusFailed, err.Error()
		return pr
	}
	if err := verifyOrigin(in.Path, m.Repository); err != nil {
		pr.Status, pr.Reason = store.StatusFailed, err.Error()
		return pr
	}

	stepErr := runPipeline(ctx, m.PostDeploy, exec, &pr)
	if stepErr == nil {
		pr.Status = store.StatusSuccess
		return pr
	}

	pr.Reason = stepErr.Error()

	if !m.RollbackOnFailure {
		pr.Status = store.StatusFailed
		return pr
	}

	// The rollback runs detached from the deploy's context and on its own budget.
	// ctx is already cancelled whenever the deploy was interrupted rather than
	// merely failed — Ctrl+C, or a drain past its budget — and privexec cannot
	// start a process on a cancelled context, so the safety net would be dead in
	// one of the cases that most needs it, leaving the tree half-reset. Each step
	// still carries its own timeout; this only bounds the whole.
	rbCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancelRollback()

	if err := rollbackTo(rbCtx, pr.PreviousSHA, exec, &pr); err != nil {
		pr.Status = store.StatusFailed
		pr.Reason = stepErr.Error() + "; rollback failed: " + err.Error()

		return pr
	}

	pr.Status = store.StatusRolledBack

	return pr
}

// rollbackPath resets one installation to the commit opts.RollbackSHAs maps for
// it. Unlike a deploy it never fetches: the commit is already in the checkout's
// object store, since it is the one that was deployed there before.
//
// A checkout with no mapped commit is not this rollback's business — the last
// deploy skipped it or it was added since — so it is left exactly where it is.
// Resetting it to anything would move a tree the operator never asked about, and
// the only commit on hand belongs to another checkout.
func rollbackPath(ctx context.Context, in discover.Installation, opts Options) PathResult {
	pr := newPathResult(in)

	if in.Err != nil {
		pr.Status, pr.Reason = store.StatusFailed, in.Err.Error()
		return pr
	}

	sha, ok := opts.RollbackSHAs[in.Path]
	if !ok {
		pr.Status = store.StatusSkipped
		pr.Reason = "the last deploy did not move this checkout — nothing to roll back here"

		return pr
	}

	exec, cleanup, err := prepare(ctx, in, opts)
	if err != nil {
		pr.Status, pr.Reason = store.StatusFailed, err.Error()
		return pr
	}
	defer cleanup()

	if pr.PreviousSHA, err = headSHA(ctx, exec); err != nil {
		pr.Status, pr.Reason = store.StatusFailed, err.Error()
		return pr
	}

	// A checkout already at its target has nothing to undo — leave it, pipeline
	// included. The killed-deploy fallback maps every recorded checkout to its own
	// last commit, so a checkout the kill never moved is mapped to where it already
	// sits; without this the reset is a no-op but the pipeline re-runs, firing a
	// migration or a cache rebuild on a tree the operator did not mean to touch. On
	// the ordinary path the target is always the checkout's previous commit and it
	// has always moved since, so this never fires there.
	if sha == pr.PreviousSHA {
		pr.Status = store.StatusSkipped
		pr.Reason = "already at its rollback target — nothing to undo here"

		return pr
	}

	if err := rollbackTo(ctx, sha, exec, &pr); err != nil {
		pr.Status, pr.Reason = store.StatusFailed, err.Error()
		return pr
	}

	// An explicit rollback that reached its commit and re-ran the tree's pipeline
	// is a success. StatusRolledBack means something else: a deploy that failed
	// and was undone.
	pr.Status = store.StatusSuccess

	return pr
}

// newPathResult carries what discovery already knows about the installation into
// its result, so a path that fails immediately still names its owner.
func newPathResult(in discover.Installation) PathResult {
	return PathResult{
		Path:      in.Path,
		User:      in.User,
		UID:       in.UID,
		GID:       in.GID,
		RanAsRoot: in.RanAsRoot,
	}
}

// prepare locks the working tree and builds the options every git and every
// post_deploy command runs under: as the directory's owner, with the deploy key
// served by an ephemeral agent. The returned cleanup closes the agent and
// releases the lock.
func prepare(ctx context.Context, in discover.Installation, opts Options) (privexec.Options, func(), error) {
	release, err := Lock(ctx, opts.LocksDir, in.Path)
	if err != nil {
		return privexec.Options{}, nil, err
	}
	cleanup := release

	// Owner detection is the stat discovery already did, resolved to a passwd
	// entry here: privexec needs HOME and the supplementary groups from it.
	owner, err := user.LookupId(strconv.Itoa(in.UID))
	if err != nil {
		cleanup()
		return privexec.Options{}, nil, fmt.Errorf("unknown owner uid %d of %s: %w", in.UID, in.Path, err)
	}

	exec := privexec.Options{Dir: in.Path, User: owner, Stream: opts.Stream}

	// The private key never touches the site user's disk: it is served from an
	// ephemeral agent whose socket dies with this deploy. A repository with no
	// key on this server deploys without one — that is what a public checkout
	// over HTTPS needs, and what the tests exercise.
	//
	// Keyed on opts.Repository, the registered slug, because that is the exact
	// string `repo add` saved the key under and sshkey.Path is a case-sensitive
	// filename. in.Repository comes from the checkout's origin, which GitHub
	// resolves case-insensitively, so it can differ in case from the registration
	// — and the resulting miss is indistinguishable from "no key on this server",
	// silently deploying without one. targets() only admits installations whose
	// origin EqualFolds opts.Repository, so the two name the same repository.
	key, err := sshkey.Load(opts.KeysDir, opts.Repository)
	switch {
	case err == nil:
		ag, err := sshkey.StartAgent(key.Private, in.UID, in.GID)
		if err != nil {
			cleanup()
			return privexec.Options{}, nil, err
		}
		cleanup = func() {
			_ = ag.Close()
			release()
		}

		exec.Env = append(exec.Env,
			"SSH_AUTH_SOCK="+ag.Socket(),
			"GIT_SSH_COMMAND="+sshkey.GitSSHCommand(ag.Socket(), opts.KnownHosts),
		)

		// The deploy key only authenticates over SSH: a checkout cloned with
		// `git clone https://…` would ignore it and fail on a private repository.
		// Only worth rewriting once there is a key to authenticate with — without
		// one it would turn a working public checkout into an SSH fetch that can
		// never succeed.
		if in.RemoteHTTPS {
			if err := useSSHRemote(ctx, opts.Repository, exec); err != nil {
				cleanup()
				return privexec.Options{}, nil, err
			}
		}

	case errors.Is(err, os.ErrNotExist):
		// No key on this server. The checkout still deploys — that is what a public
		// repository needs — but an SSH origin must not fall back to the site user's
		// ssh config for its host keys, so it is pinned with no agent offered. An
		// HTTPS origin needs no ssh at all and is left alone.
		if !in.RemoteHTTPS {
			exec.Env = append(exec.Env, "GIT_SSH_COMMAND="+sshkey.GitSSHCommand("", opts.KnownHosts))
		}

	default:
		cleanup()
		return privexec.Options{}, nil, err
	}

	return exec, cleanup, nil
}

// runPipeline runs the post_deploy steps in order, recording each. It stops at
// the first failure unless the step sets continue_on_failure. Every step gets a
// real timeout — an old implementation parses the timeout and never applies it, so a
// cold `composer install` dies at Laravel's 60s default.
func runPipeline(ctx context.Context, steps []manifest.Step, exec privexec.Options, pr *PathResult) error {
	for _, s := range steps {
		o := exec
		o.Timeout = s.Timeout

		res, err := privexec.Run(ctx, s.Run, o)
		pr.Commands = append(pr.Commands, CommandResult{
			Command:  res.Command,
			ExitCode: res.ExitCode,
			Duration: res.Duration,
			Output:   res.Output,
			TimedOut: res.TimedOut,
		})

		if err != nil && !s.ContinueOnFailure {
			return err
		}
	}

	return nil
}

// rollbackTo resets the tree to sha and re-runs the manifest that tree carries —
// the previous manifest, since the manifest versions with the code.
func rollbackTo(ctx context.Context, sha string, exec privexec.Options, pr *PathResult) error {
	if err := resetTo(ctx, sha, exec); err != nil {
		return err
	}
	pr.NewSHA = sha

	m, err := manifest.Load(exec.Dir)
	if err != nil {
		return err
	}

	return runPipeline(ctx, m.PostDeploy, exec, pr)
}

// verifyOrigin re-checks the pulled manifest against the checkout's origin. The
// pre-deploy scan already did this, but the manifest that runs is the one the
// pull brought in, and that is the one whose claim must hold.
func verifyOrigin(dir, repository string) error {
	slug, _, err := discover.OriginSlug(dir)
	if err != nil {
		return err
	}
	if !strings.EqualFold(slug, repository) {
		return fmt.Errorf("%s: the pulled manifest declares %s but origin is %s", dir, repository, slug)
	}

	return nil
}

// branchOf returns the branch a ref names, or "" for a manual deploy (no ref) or
// a ref that is not a branch — a tag push deploys nothing.
func branchOf(ref string) string {
	if ref == "" {
		return ""
	}

	branch, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok {
		return ""
	}

	return branch
}
