package cmd

import (
	"context"
	"fmt"

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
			"a GitHub token with write access is the whole requirement, which is what lets it run from a laptop.",
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
		return fmt.Errorf("no webhook on %s delivers `repository_dispatch`, so a setup request would reach no server — run `rec-deploy repo check %s --repair` on each server that deploys it", slug, slug)
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
		ui.Warn(plural(stale, "webhook") + " on this repository does not deliver `repository_dispatch` and will not receive it — run `rec-deploy repo check " + slug + " --repair` on the server behind it")
	}
	ui.Info("each server reports the result through its own notifications; `rec-deploy logs " + slug + "` shows it there")

	return nil
}

// dispatchReach counts the repository's webhooks that would receive a dispatch,
// and those that would not. An inactive hook delivers nothing whatever it
// subscribes to, so it counts as unreachable.
//
// It returns counts and never the hooks themselves: a webhook URL's path segment
// is the delivery token of the server behind it, and this command runs against
// repositories whose other servers are none of the operator's business.
func dispatchReach(hooks []github.Hook) (ready, stale int) {
	for _, h := range hooks {
		if h.Active && h.Delivers("repository_dispatch") {
			ready++
			continue
		}
		stale++
	}

	return ready, stale
}
