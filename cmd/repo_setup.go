package cmd

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newRepoSetupCmd builds `repo setup owner/repo`: ask every server registered on
// the repository to run its setup pipeline.
func newRepoSetupCmd() *cobra.Command {
	var branch string

	cmd := &cobra.Command{
		Use:   "setup <owner/repo>",
		Short: "Ask every server registered on a repository to run its setup pipeline",
		Long: "setup sends GitHub a repository_dispatch, which GitHub delivers to every server registered on the repository — " +
			"so the setup block runs without an SSH session anywhere. It reads no local state and needs no registered repository: " +
			"a GitHub token with write access is the whole requirement, which is what lets it run from a laptop. " +
			"In a terminal, with no --branch given, it first asks whether to run here instead — the two differ by blast radius, so nothing is guessed on the operator's behalf.",
		Args: cobra.MaximumNArgs(1),
		Example: "rec-deploy repo setup rdcstarr/tema-mea\n" +
			"rec-deploy repo setup rdcstarr/tema-mea --branch develop",
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, ok, err := interactiveArg(args, "Repository (owner/repo)")
			if err != nil {
				return err
			}
			if !ok {
				return cmd.Help()
			}

			// A flag names the scope outright. Without one, in a terminal, ask:
			// the two triggers differ by blast radius, which is not a default to
			// guess at on the operator's behalf.
			if !isInteractive() || cmd.Flags().Changed("branch") {
				return requestSetup(cmd.Context(), slug, branch)
			}

			where, err := selectMenu("Where should setup run?", []ui.Option{
				{Label: "this server", Value: "local"},
				{Label: "every server registered on this repository", Value: "fleet"},
			})
			if err != nil {
				return err
			}
			if where == "" {
				// Backed out (Esc / ←) — climb to the repo hub, run nothing.
				return ui.ErrBack
			}
			if where == "local" {
				return runDeploy(cmd.Context(), slug, "", true)
			}

			return requestSetup(cmd.Context(), slug, branch)
		},
	}

	cmd.Flags().StringVar(&branch, "branch", "", "only the checkouts on this branch")

	return cmd
}

// requestSetup sends the dispatch, after proving it has somewhere to land.
//
// A dispatch sent to a repository whose hooks all predate this feature is
// answered 204 by GitHub and does nothing, on any server — a success report for
// work that will never happen. So the hooks are counted first, and a repository
// no server can be reached through is an error rather than a checkmark.
func requestSetup(ctx context.Context, slug, branch string) error {
	if err := github.ValidateSlug(slug); err != nil {
		return err
	}

	client, err := githubClient(ctx)
	if err != nil {
		return err
	}

	var hooks []github.Hook
	if err := ui.Spinner("Reading the webhooks on GitHub…", func() error {
		hooks, err = client.Hooks(ctx, slug)

		return err
	}); err != nil {
		return err
	}

	ready, stale := dispatchReach(hooks)
	if ready == 0 {
		return fmt.Errorf("no rec-deploy webhook on %s delivers `repository_dispatch`, so a setup request would reach no server — run `rec-deploy repo check %s --repair` on each server that deploys it", slug, slug)
	}

	target := "every checkout"
	if branch != "" {
		target = "the checkouts on " + branch
	}

	if !flagYes {
		if !isInteractive() {
			return fmt.Errorf("setup runs the install steps on %s of %s, on %s — re-run with `--yes`", target, slug, plural(ready, "server"))
		}

		ok, err := ui.Confirm(
			"Run setup for "+slug+" on "+plural(ready, "server")+"?",
			"github delivers this to every server registered on the repository; it runs "+target+" there",
		)
		if err != nil {
			return err
		}
		if !ok {
			// Declining is a back-out, not a completed request.
			return ui.ErrBack
		}
	}

	if err := ui.Spinner("Asking GitHub to deliver the request…", func() error {
		return client.Dispatch(ctx, slug, github.DispatchSetup, branch)
	}); err != nil {
		return err
	}

	if flagJSON {
		return ui.PrintJSON(map[string]any{
			"repository":  slug,
			"branch":      branch,
			"event_type":  github.DispatchSetup,
			"servers":     ready,
			"unreachable": stale,
		})
	}

	ui.Success("setup requested for " + slug + " — github delivers it to " + plural(ready, "server"))
	if stale > 0 {
		ui.Warn(plural(stale, "rec-deploy webhook") + " on this repository can't deliver `repository_dispatch` and won't receive this request — the server behind each one fixes its own with `rec-deploy repo check " + slug + " --repair`")
	}
	ui.Info("each server reports the result through its own notifications; `rec-deploy logs " + slug + "` shows it there")

	return nil
}

// dispatchReach counts rec-deploy's own webhooks on the repository: those that
// would receive a dispatch, and those that would not. An inactive hook delivers
// nothing whatever it subscribes to, so it counts as unreachable.
//
// It returns counts and never the hooks themselves: a webhook URL's path segment
// is the delivery token of the server behind it, and this command runs against
// repositories whose other servers are none of the operator's business.
func dispatchReach(hooks []github.Hook) (ready, stale int) {
	for _, h := range hooks {
		// Someone else's webhook answers for no rec-deploy server. Counted
		// ready it would prove a dispatch lands where nothing runs; counted
		// stale it would send the operator to repair a server that does not
		// exist behind a Slack app.
		if !recDeployHook(h) {
			continue
		}
		if h.Active && h.Delivers("repository_dispatch") {
			ready++
			continue
		}
		stale++
	}

	return ready, stale
}

// recDeployHook reports whether a webhook is one rec-deploy registered, from the
// listing alone and without printing any part of it. github.HookURL builds every
// one of them as <public_url>/hook/<token>, so a `/hook/` segment followed by a
// token is the signature — and it holds for a public_url that carries a path of
// its own.
//
// It is a heuristic: GitHub's API does not say which tool created a hook. But it
// is a far better one than the alternative it replaces, which was to assume that
// every webhook on the repository is a rec-deploy server.
func recDeployHook(h github.Hook) bool {
	u, err := url.Parse(strings.TrimSpace(h.URL))
	if err != nil {
		return false
	}
	dir, token := path.Split(u.Path)

	return token != "" && strings.HasSuffix(dir, "/hook/")
}
