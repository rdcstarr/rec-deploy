package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/deploy"
	"github.com/rdcstarr/rec-deploy/internal/store"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newDeployCmd builds `deploy owner/repo`: deploy now, streaming the pipeline.
func newDeployCmd() *cobra.Command {
	var path string

	cmd := &cobra.Command{
		Use:     "deploy <owner/repo>",
		Short:   "Deploy a repository now",
		Long:    "deploy runs the pipeline for every checkout of the repository on this server, on the branch each checkout is on.",
		Args:    cobra.MaximumNArgs(1),
		Example: "rec-deploy deploy rdcstarr/tema-mea\nrec-deploy deploy rdcstarr/tema-mea --path /var/www/api",
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, ok, err := pickRepo(cmd.Context(), args, "Repository to deploy")
			if err != nil {
				return err
			}
			if !ok {
				return cmd.Help()
			}

			return runDeploy(cmd.Context(), slug, path)
		},
	}

	cmd.Flags().StringVar(&path, "path", "", "deploy only this checkout")

	return cmd
}

// newRollbackCmd builds `rollback owner/repo`: reset every checkout to the
// commit it was on before the last deploy.
func newRollbackCmd() *cobra.Command {
	var path string

	cmd := &cobra.Command{
		Use:     "rollback <owner/repo>",
		Short:   "Reset a repository's checkouts to their previous commit",
		Long:    "rollback resets every checkout of the repository to the commit it was on before the last deploy, then re-runs the pipeline of the tree it lands on — the manifest versions with the code, so the previous commit's pipeline is the right one to run.",
		Args:    cobra.MaximumNArgs(1),
		Example: "rec-deploy rollback rdcstarr/tema-mea\nrec-deploy rollback rdcstarr/tema-mea --path /var/www/api --yes",
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, ok, err := pickRepo(cmd.Context(), args, "Repository to roll back")
			if err != nil {
				return err
			}
			if !ok {
				return cmd.Help()
			}

			return runRollback(cmd.Context(), slug, path)
		},
	}

	cmd.Flags().StringVar(&path, "path", "", "roll back only this checkout")

	return cmd
}

// runDeploy deploys slug, streaming the pipeline to stdout — a deploy shows its
// output live, so it never wraps in a spinner. It passes no Ref: every checkout
// deploys the branch it is on.
func runDeploy(ctx context.Context, slug, path string) error {
	cfg := Config()

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	repo, err := registeredRepo(ctx, st, slug)
	if err != nil {
		return err
	}

	// The deploy pulls over SSH, so github.com's host keys have to be pinned
	// before git talks to it — never StrictHostKeyChecking=no. It reaches the
	// network, so it spins rather than sitting on a dead pause.
	if err := ui.Spinner("Pinning github.com host keys…", func() error {
		return pinHostKeys(ctx)
	}); err != nil {
		return err
	}

	opts, err := deployOptions(cfg, repo.Repository, path)
	if err != nil {
		return err
	}
	if !flagJSON {
		opts.Stream = os.Stdout
		ui.Title("deploying " + repo.Repository)
	}

	deployID, err := st.DeployStart(ctx, store.Deploy{
		RepoID: repo.ID,
		Status: store.StatusRunning,
	})
	if err != nil {
		return err
	}

	res, runErr := deploy.Run(ctx, opts)
	record(ctx, st, cfg, deployID, res, runErr)

	return report(res, runErr)
}

// runRollback resets every checkout of slug to the commit the last deploy moved
// it off, and re-runs the pipeline of the tree it lands on. It is destructive —
// the working tree is reset hard — so it confirms in a terminal and demands
// --yes anywhere else. Like a deploy, it streams.
func runRollback(ctx context.Context, slug, path string) error {
	cfg := Config()

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	repo, err := registeredRepo(ctx, st, slug)
	if err != nil {
		return err
	}

	opts, err := deployOptions(cfg, repo.Repository, path)
	if err != nil {
		return err
	}

	// The engine keeps no history of its own: each checkout's target is the commit
	// the last deploy recorded it at before it moved it, resolved per checkout so
	// none is reset onto a sibling's commit.
	opts.RollbackSHAs, err = rollbackTargets(ctx, st, repo.Repository, opts.Path)
	if err != nil {
		return err
	}
	n := len(opts.RollbackSHAs)

	if !flagYes {
		if !isInteractive() {
			return fmt.Errorf("rollback resets %s of %s to their previous commits — re-run with `--yes`", plural(n, "checkout"), slug)
		}

		ok, err := ui.Confirm("Roll "+slug+" back — reset "+plural(n, "checkout")+" to the previous commit?", describeRollback(opts.RollbackSHAs))
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	if !flagJSON {
		opts.Stream = os.Stdout
		ui.Title("rolling " + repo.Repository + " back — " + plural(n, "checkout"))
	}

	deployID, err := st.DeployStart(ctx, store.Deploy{
		RepoID: repo.ID,
		Status: store.StatusRunning,
	})
	if err != nil {
		return err
	}

	// A rollback reaches for no commit it does not already have, so it needs
	// neither the network nor a pin refresh: the target is in the checkout's own
	// object store, put there by the deploy that is being undone.
	res, runErr := deploy.Rollback(ctx, opts)
	record(ctx, st, cfg, deployID, res, runErr)

	return report(res, runErr)
}

// deployOptions builds the engine options a manual run shares with the daemon's:
// the discovery roots from the config, and the state paths the deploy key, the
// lock and the pinned host keys live under.
func deployOptions(cfg *config.Config, slug, path string) (deploy.Options, error) {
	keysDir, err := config.KeysDir()
	if err != nil {
		return deploy.Options{}, err
	}
	locksDir, err := config.LocksDir()
	if err != nil {
		return deploy.Options{}, err
	}
	knownHosts, err := config.KnownHostsFile()
	if err != nil {
		return deploy.Options{}, err
	}

	// The engine matches a target by the absolute path discovery reports, so
	// --path ./site and a trailing slash have to resolve to that same string.
	if path != "" {
		if path, err = filepath.Abs(path); err != nil {
			return deploy.Options{}, err
		}
	}

	roots := append([]string(nil), cfg.Discovery.Roots...)
	if path != "" {
		// An explicitly selected checkout is authoritative even outside the
		// configured roots. This also lets repo install deploy a fresh clone.
		roots = append(roots, path)
	}

	return deploy.Options{
		Repository: slug,
		Path:       path,
		Roots:      roots,
		Prune:      cfg.Discovery.Prune,
		KeysDir:    keysDir,
		LocksDir:   locksDir,
		KnownHosts: knownHosts,
	}, nil
}

// record persists and notifies a finished run. Ctrl+C during a deploy cancels
// ctx, and the tree it interrupted may be half-moved: recording that under the
// cancelled context would drop it, so the report gets a context of its own —
// bounded, since a notification channel must not hang the command.
func record(ctx context.Context, st *store.Store, cfg *config.Config, deployID int64, res deploy.Result, runErr error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	recordResult(ctx, st, cfg, deployID, res, runErr)
}

// report renders a finished run and returns its error unchanged, so a failed
// deploy prints every path it touched and still exits non-zero.
func report(res deploy.Result, runErr error) error {
	if flagJSON {
		if err := ui.PrintJSON(res); err != nil {
			return err
		}

		return runErr
	}

	renderResult(res)

	return runErr
}

// renderResult prints one line per checkout: the commit it landed on, who it ran
// as, and why it skipped or failed. A root-owned target is flagged here as it is
// everywhere else — push access to that repository is root on this server.
func renderResult(res deploy.Result) {
	for _, pr := range res.Paths {
		line := strings.TrimRight(pr.Path+"  "+strings.Join(resultFlags(pr), "  "), " ")

		switch pr.Status {
		case store.StatusSuccess:
			ui.Success(line)
		case store.StatusSkipped:
			ui.Info(line)
		default:
			ui.Warn(line)
		}
	}
}

// resultFlags describes one checkout's outcome: its status when that is not
// plain success, the user the commands ran as, the commit move, and the reason
// it skipped or failed.
func resultFlags(pr deploy.PathResult) []string {
	var flags []string
	if pr.Status != store.StatusSuccess {
		flags = append(flags, pr.Status)
	}
	if pr.User != "" {
		flags = append(flags, pr.User)
	}
	if pr.RanAsRoot {
		flags = append(flags, "⚠ root")
	}

	switch {
	case pr.NewSHA != "" && pr.NewSHA != pr.PreviousSHA:
		flags = append(flags, shortSHA(pr.PreviousSHA)+" → "+shortSHA(pr.NewSHA))
	case pr.NewSHA != "":
		flags = append(flags, shortSHA(pr.NewSHA)+" (unchanged)")
	}

	if pr.Reason != "" {
		flags = append(flags, pr.Reason)
	}

	return flags
}

// rollbackTargets resolves, per checkout, the commit a rollback must reset it to:
// the commit the last deploy of slug recorded that checkout at before it moved
// it. Only the checkouts the last deploy actually moved are returned, each mapped
// to its own previous commit — a checkout the deploy skipped by the branch filter,
// or one added since, has no entry, so the engine leaves it where it is instead of
// dragging it onto a sibling's commit.
func rollbackTargets(ctx context.Context, st *store.Store, slug, path string) (map[string]string, error) {
	deploys, err := st.Deploys(ctx, slug, 1)
	if err != nil {
		return nil, err
	}
	if len(deploys) == 0 {
		return nil, fmt.Errorf("%s has never been deployed from this server, so there is no commit to roll back to — deploy it first with `rec-deploy deploy %s`", slug, slug)
	}

	paths, err := st.DeployPaths(ctx, deploys[0].ID)
	if err != nil {
		return nil, err
	}

	// The ordinary case: the last deploy recorded what it moved. Take each moved
	// checkout's previous commit; a skipped one carries an empty previous_sha and
	// is simply not a target.
	if len(paths) > 0 {
		targets := targetsFrom(paths, path, func(p store.DeployPath) string { return p.PreviousSHA })
		if len(targets) > 0 {
			return targets, nil
		}
		if path != "" {
			return nil, fmt.Errorf("the last deploy of %s did not move %s, so there is nothing to roll back there — `rec-deploy logs %s` shows what it did", slug, path, slug)
		}

		return nil, fmt.Errorf("the last deploy of %s moved no checkout, so there is nothing to roll back — `rec-deploy logs %s` shows what it did", slug, slug)
	}

	// No path rows at all. Only a deploy killed between starting and recording
	// leaves that — an OOM during a build, or systemd's SIGKILL at the end of a
	// drain — and the tree was already moved before the kill, so this is the case
	// the fallback exists for. Gate on the status: a deploy that merely failed
	// recorded nothing and never touched a tree, so it has nothing to undo.
	if s := deploys[0].Status; s != store.StatusRunning && s != store.StatusInterrupted {
		return nil, fmt.Errorf("the last deploy of %s recorded no checkout to roll back — `rec-deploy logs %s` shows what it did", slug, slug)
	}

	// Reach back to where each checkout was last recorded — its prior new_sha,
	// which is where the killed deploy found it. LastDeployPerPathIn already drops
	// rows with no new_sha, so a skipped checkout brings no false target.
	prior, err := st.LastDeployPerPathIn(ctx, slug)
	if err != nil {
		return nil, err
	}
	targets := targetsFrom(prior, path, func(p store.DeployPath) string { return p.NewSHA })
	if len(targets) == 0 {
		return nil, fmt.Errorf("the last deploy of %s was interrupted before it recorded anything, and no earlier deploy offers a commit to return to — reset the checkout by hand with `git reset --hard HEAD@{1}`", slug)
	}

	return targets, nil
}

// targetsFrom maps each path row to the commit sha(p) returns, dropping rows with
// no commit and — when only is set — every path but that one.
func targetsFrom(paths []store.DeployPath, only string, sha func(store.DeployPath) string) map[string]string {
	out := map[string]string{}
	for _, p := range paths {
		if only != "" && p.Path != only {
			continue
		}
		if s := sha(p); s != "" {
			out[p.Path] = s
		}
	}

	return out
}

// describeRollback lists each checkout and the commit it will be reset to, for
// the confirmation prompt — a bare count would hide which trees move and onto
// what.
func describeRollback(targets map[string]string) string {
	paths := make([]string, 0, len(targets))
	for p := range targets {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var b strings.Builder
	for _, p := range paths {
		b.WriteString("\n  " + p + " → " + shortSHA(targets[p]))
	}

	return b.String()
}

// shortSHA abbreviates a commit for human output.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}

	return sha
}
