package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/discover"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newRepoSetupCmd builds `repo setup owner/repo`: run the setup pipeline over
// this server's own checkouts of the repository.
//
// It ran fleet-wide until v0.16.1, by asking GitHub to deliver a
// repository_dispatch to every server registered on the repository. That
// transport does not exist: repository_dispatch is a GitHub-App-only webhook
// event, so a repository webhook registered with a personal access token may
// not subscribe to it — and merely naming it in the events array took `repo
// add` and `repo rotate` down with a 422. The fan-out was removed rather than
// re-plumbed; `repo deploy --setup` already covers a server, and every
// replacement transport (a tag as the carrier, a GitHub App) costs more than
// the reach was worth.
func newRepoSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup <owner/repo>",
		Short: "Run the setup pipeline on this server's checkouts of a repository",
		Long: "setup runs the manifest's setup block and then post_deploy, over every checkout of the repository on this server. " +
			"It is the first-install counterpart of a push, which runs post_deploy alone. " +
			"Setup steps are frequently not idempotent — `php artisan key:generate` invalidates every session and everything encrypted with the old key — " +
			"so it confirms in a terminal and needs `--yes` anywhere else. " +
			"Narrow it to a single checkout with `rec-deploy repo deploy <owner/repo> --setup --path <p>`.",
		Args: cobra.MaximumNArgs(1),
		Example: "rec-deploy repo setup rdcstarr/tema-mea\n" +
			"rec-deploy repo setup rdcstarr/tema-mea --yes",
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, ok, err := interactiveArg(args, "Repository (owner/repo)")
			if err != nil {
				return err
			}
			if !ok {
				return cmd.Help()
			}

			return runLocalSetup(cmd.Context(), slug)
		},
	}
}

// runLocalSetup confirms, then runs the setup pipeline over this server's own
// checkouts of slug.
//
// Setup steps are frequently not idempotent, so this is as outward-facing as
// any other destructive action and takes the same shape: confirm in a terminal,
// demand --yes anywhere else. The scan behind the confirmation runs only when
// there is somebody to show it to.
//
// It grows no branch flag: `repo deploy <slug> --setup --path <p>` already
// narrows to one checkout, and the confirmation says so.
func runLocalSetup(ctx context.Context, slug string) error {
	if !flagYes {
		if !isInteractive() {
			return fmt.Errorf("setup runs the install steps on every checkout of %s on this server — re-run with `--yes`, or narrow it with `rec-deploy repo deploy %s --setup --path <p>`", slug, slug)
		}

		found := localCheckouts(ctx, slug)

		// A cancelled scan is the operator leaving, not discovery coming up empty —
		// localCheckouts cannot tell the two apart, by design, since every other
		// failure is only an offer it could not make.
		if err := ctx.Err(); err != nil {
			return err
		}

		ok, err := ui.Confirm("Run setup for "+slug+" on this server?", describeLocalSetup(found, slug))
		if err != nil {
			return err
		}
		if !ok {
			// Declining is a back-out, not a completed run.
			return ui.ErrBack
		}
	}

	return runDeploy(ctx, slug, "", true)
}

// describeLocalSetup lists the checkouts a local setup run will reach, each with
// the branch it is on, for the confirmation prompt — a bare "this server" hides
// that production is one of them.
//
// Discovery is an offer in this command, never a requirement, so a scan that
// answers nothing still has to describe the run rather than draw an empty
// prompt: the engine discovers again for itself, and finding nothing there is
// its own reported error.
func describeLocalSetup(found []discover.Installation, slug string) string {
	var b strings.Builder
	for _, in := range found {
		b.WriteString("\n  " + in.Path)
		if in.Branch != "" {
			b.WriteString(" (" + in.Branch + ")")
		}
	}
	if b.Len() == 0 {
		b.WriteString("\n  every checkout of " + slug + " on this server")
	}
	b.WriteString("\n\nnarrow it to one with `rec-deploy repo deploy " + slug + " --setup --path <p>`")

	return b.String()
}

// localCheckouts returns this server's own checkouts of slug, and none when
// discovery cannot answer. It feeds the confirmation prompt alone: a scan that
// finds nothing, or fails outright, narrows what can be described rather than
// ending the command, because the engine discovers again for itself.
func localCheckouts(ctx context.Context, slug string) []discover.Installation {
	found, err := scanInstallations(ctx)
	if err != nil {
		return nil
	}

	return discover.Filter(found, slug)
}
