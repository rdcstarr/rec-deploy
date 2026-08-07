package cmd

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/discover"
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
			"so the setup block runs without an SSH session anywhere. It needs no store and no registered repository: " +
			"a GitHub token with write access is the whole requirement, which is what lets it run from a laptop. " +
			"In a terminal, with no --branch given, it first asks whether to run here instead, then which branch the request should reach — the triggers differ by blast radius and a setup step is rarely safe to repeat, so nothing is guessed on the operator's behalf.",
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
				return runLocalSetup(cmd.Context(), slug)
			}

			// The fleet arm, and only it, then asks the branch: the dispatch
			// reaches every server registered on the repository, and a setup
			// step is frequently not idempotent, so "every checkout everywhere"
			// must be a choice the operator makes rather than the only one on
			// offer.
			chosen, err := chooseSetupBranch(cmd.Context(), slug)
			if err != nil {
				return err
			}

			return requestSetup(cmd.Context(), slug, chosen)
		},
	}

	cmd.Flags().StringVar(&branch, "branch", "", "only the checkouts on this branch")

	return cmd
}

// runLocalSetup confirms, then runs the setup pipeline over this server's own
// checkouts of slug.
//
// The fleet arm asks a branch and then confirms; this arm reached the engine
// with neither, so a server holding staging on develop and production on main
// ran the install steps on production from two menu picks. Setup steps are
// frequently not idempotent — `php artisan key:generate` invalidates every
// session and everything encrypted with the old key — so a local run is as
// outward-facing as a dispatch and owes the same confirmation.
//
// It grows no branch flag of its own: `repo deploy <slug> --setup --path <p>`
// already narrows to one checkout, and the confirmation says so.
func runLocalSetup(ctx context.Context, slug string) error {
	found := localCheckouts(ctx, slug)

	// A cancelled scan is the operator leaving, not discovery coming up empty —
	// the same distinction chooseSetupBranch draws, for the same reason.
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

// branchEvery and branchOther are the two answers of the branch chooser that
// are not branch names. Both carry a space, which git forbids in a ref, so
// neither can collide with a branch an operator really has.
const (
	branchEvery = "every branch"
	branchOther = "another branch"
)

// chooseSetupBranch asks which branch a fleet-wide setup should reach, and
// returns it — "" for every branch, which is what Dispatch sends when no branch
// is named.
//
// It is the mitigation for the sharpest edge in this design: one dispatch
// reaches every server registered on the repository, and install steps are
// frequently not idempotent — `php artisan key:generate` invalidates every
// session and everything encrypted with the old key. An operator who is offered
// no way to narrow is offered only the destructive option.
func chooseSetupBranch(ctx context.Context, slug string) (string, error) {
	local := localCheckouts(ctx, slug)

	// A cancelled scan is the operator leaving, not discovery coming up empty.
	// localCheckouts cannot tell the two apart — by design, since every other
	// failure is only an offer it could not make — so Ctrl+C during the scan
	// would otherwise draw this chooser over the interrupt and need a second one
	// to escape. Surfacing the error is what every other caller of a cancelled
	// scan already does.
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// selectMenu, like the scope question above it: both are navigation screens
	// of the same flow, so h shows the command's help on either.
	choice, err := selectMenu("Which branch should run setup?", setupBranchOptions(local))
	if err != nil {
		return "", err // ui.ErrBack (re-show the hub) or ui.ErrQuit (quit)
	}

	switch choice {
	case "":
		// Backed out (Esc / ←) — climb, dispatch nothing.
		return "", ui.ErrBack
	case branchEvery:
		return "", nil
	case branchOther:
		branch, ok, err := interactiveArg(nil, "Branch name")
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ui.ErrBack
		}

		return branch, nil
	}

	return choice, nil
}

// setupBranchOptions builds the branch chooser: every branch, then each
// distinct branch this server's own checkouts of the repository sit on, then
// one typed by hand.
//
// The local branches are not the fleet's answer — no server knows what the
// others hold — but they are the honest set to offer, and they beat typing a
// branch name blind into a command that reaches every machine. The hand-typed
// entry is what keeps this usable where discovery answers nothing, which is
// every laptop: `repo setup` requires no local state by design, and a chooser that
// could only offer "every branch" there would put the operator back on the one
// option this exists to stop being mandatory.
func setupBranchOptions(found []discover.Installation) []ui.Option {
	var branches []string
	for _, in := range found {
		if in.Branch != "" && !slices.Contains(branches, in.Branch) {
			branches = append(branches, in.Branch)
		}
	}
	slices.Sort(branches)

	options := make([]ui.Option, 0, len(branches)+2)
	options = append(options, ui.Option{Label: "every branch — every checkout, on every server", Value: branchEvery})
	for _, b := range branches {
		options = append(options, ui.Option{Label: b + " — the checkouts on this branch", Value: b})
	}

	return append(options, ui.Option{Label: "another branch…", Value: branchOther})
}

// localCheckouts returns this server's own checkouts of slug, and none when
// discovery cannot answer. Discovery is an offer here, never a requirement:
// `repo setup` needs no store and no registered repository — that is what lets
// it run from a laptop — so a scan that finds nothing, or fails outright,
// narrows what can be offered instead of ending the command.
func localCheckouts(ctx context.Context, slug string) []discover.Installation {
	found, err := scanInstallations(ctx)
	if err != nil {
		return nil
	}

	return discover.Filter(found, slug)
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

	// Hooks reads one page, so a full page back means GitHub had more to give
	// and every count derived from it is a floor.
	truncated := len(hooks) == github.HooksPerPage

	target := "every checkout"
	if branch != "" {
		target = "the checkouts on " + branch
	}

	if !flagYes {
		if !isInteractive() {
			return fmt.Errorf("setup runs the install steps on %s of %s, on %s — re-run with `--yes`", target, slug, serverCount(ready, truncated))
		}

		ok, err := ui.Confirm(
			"Run setup for "+slug+" on "+serverCount(ready, truncated)+"?",
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
			// GitHub had more webhooks than one page: servers and unreachable are
			// floors, not totals.
			"truncated": truncated,
		})
	}

	ui.Success("setup requested for " + slug + " — github delivers it to " + serverCount(ready, truncated))
	if stale > 0 {
		ui.Warn(plural(stale, "rec-deploy webhook") + " on this repository can't deliver `repository_dispatch` and won't receive this request — the server behind each one fixes its own with `rec-deploy repo check " + slug + " --repair`")
	}
	ui.Info("each server reports the result through its own notifications; `rec-deploy logs " + slug + "` shows it there")

	return nil
}

// serverCount renders how many servers a dispatch reaches. github.Hooks reads
// one page, so a listing that came back full was cut short and every count
// derived from it is a floor — printed bare, the number would simply be wrong on
// the one line an operator reads before sending a request to a whole fleet.
func serverCount(ready int, truncated bool) string {
	if truncated {
		return "at least " + plural(ready, "server")
	}

	return plural(ready, "server")
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
