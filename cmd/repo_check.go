package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/store"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newRepoCheckCmd builds `repo check owner/repo`.
func newRepoCheckCmd() *cobra.Command {
	var repair bool

	cmd := &cobra.Command{
		Use:   "check <owner/repo>",
		Short: "Check that GitHub can deliver a webhook to this server",
		Long: "check compares the webhook GitHub holds against the one this server would register today, " +
			"then triggers a ping and reports what GitHub recorded — so \"will a push actually deploy?\" is a command rather than an investigation. " +
			"--repair rewrites GitHub's copy to match, without rolling the deploy key or the webhook secret.",
		Args:    cobra.MaximumNArgs(1),
		Example: "rec-deploy repo check rdcstarr/tema-mea\nrec-deploy repo check rdcstarr/tema-mea --repair",
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, ok, err := pickRepo(cmd.Context(), args, "Repository to check")
			if err != nil {
				return err
			}
			if !ok {
				return cmd.Help()
			}

			return checkRepo(cmd.Context(), slug, repair)
		},
	}

	cmd.Flags().BoolVar(&repair, "repair", false, "rewrite github's copy of the webhook to match this server")

	return cmd
}

// checkRepo answers whether a push to slug would actually deploy. It reads
// GitHub's own copy of the webhook first — that catches an address this server
// no longer serves with no delivery and no ambiguity — and then proves the rest
// of the chain with a live ping. repair, when set, rewrites GitHub's copy to
// match this server before the result is reported — and the ping is re-run
// against that new copy, so a successful repair reports as reachable rather
// than replaying the failure it just fixed.
func checkRepo(ctx context.Context, slug string, repair bool) error {
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	repo, err := registeredRepo(ctx, st, slug)
	if err != nil {
		return err
	}

	client, err := githubClient(ctx)
	if err != nil {
		return err
	}

	publicURL, err := resolvePublicURL()
	if err != nil {
		return err
	}
	want, err := github.HookURL(publicURL, repo.Token)
	if err != nil {
		return err
	}

	var hook github.Hook
	if err := ui.Spinner("Reading the webhook on GitHub…", func() error {
		hook, err = client.Hook(ctx, repo.Repository, repo.GitHubHookID)

		return err
	}); err != nil && !errors.Is(err, github.ErrNotFound) {
		return err
	}

	// A hook deleted on GitHub by hand reads as "not found" here. That is the
	// answer, not an obstacle to getting one: VerifyHook reaches the same
	// conclusion and states it as a verdict with a fix.
	drift := hookDrift(hook, want)

	verdict := verifyWebhook(ctx, client, repo)

	if flagJSON {
		repaired := repair && len(drift) > 0
		if repaired {
			if err := repairHook(ctx, client, repo, want, drift); err != nil {
				return err
			}

			// UpdateHook always sends active: true and the URL and events
			// this server wants, so the write that just happened is the
			// truth from here — the hook read and the ping above it, both
			// taken before the repair, are now stale on exactly what it
			// changed.
			hook.Active = true
			drift = nil
			verdict = verifyWebhook(ctx, client, repo)
		}

		if err := ui.PrintJSON(map[string]any{
			"repository": repo.Repository,
			"hook_id":    repo.GitHubHookID,
			"active":     hook.Active,
			"drift":      drift,
			"repaired":   repaired,
			"webhook":    reachabilityJSON(verdict),
		}); err != nil {
			return err
		}

		return webhookExit(verdict, drift)
	}

	ui.Title(ui.ScreenPath("rec-deploy", "Repositories", "Check"))
	ui.KeyValue("repository", repo.Repository)
	ui.KeyValue("webhook", redactedHookURL(repo.Token))
	ui.Out("")

	switch {
	case hook.URL == "":
		// Nothing to compare against; the reachability verdict below carries it.
	case len(drift) == 0:
		ui.Success("github holds the webhook this server would register today")
	default:
		ui.Warn("github's copy of the webhook has drifted")
		for _, issue := range drift {
			ui.KeyValue("drift", issue)
		}

		if !repair {
			ui.KeyList("fix", []string{"rewrite it with `rec-deploy repo check " + slug + " --repair`"})
			break
		}

		if err := repairHook(ctx, client, repo, want, drift); err != nil {
			return err
		}

		// Same as the --json branch above: the repair just wrote active:
		// true and this server's own URL and events, so both the hook and
		// the reachability verdict read from before it are stale.
		hook.Active = true
		drift = nil
		verdict = verifyWebhook(ctx, client, repo)
	}

	ui.Out("")
	renderReachability(ctx, slug, publicURL, verdict)

	return webhookExit(verdict, drift)
}

// hookDrift names how GitHub's copy of a webhook differs from the one this
// server would register today.
//
// Changing `public_url` after a repository is registered re-points nothing:
// Client.UpdateHook runs only from `repo rotate`, so every existing hook keeps
// delivering to the old address and nothing says so. This is where that becomes
// visible.
func hookDrift(got github.Hook, want string) []string {
	// A hook GitHub no longer has is not a hook that drifted. Comparing against
	// its zero value would invent two issues out of one, and the reachability
	// verdict already states the real one.
	if got.URL == "" {
		return nil
	}

	var issues []string

	if got.URL != want {
		issues = append(issues, "github delivers to "+hookOrigin(got.URL)+", this server expects "+hookOrigin(want))
	}
	if !got.Active {
		issues = append(issues, "the webhook is deactivated on github, so it delivers nothing")
	}
	if !got.Delivers("repository_dispatch") {
		issues = append(issues, "github does not deliver `repository_dispatch` to this hook, so `rec-deploy repo setup` will not reach this server")
	}

	return issues
}

// hookOrigin reduces a webhook URL to what can be shown. The path carries the
// delivery token, which is never printed — a drift report an operator pastes
// into an issue must not hand it over.
func hookOrigin(hookURL string) string {
	u, err := url.Parse(strings.TrimSpace(hookURL))
	if err != nil || u.Host == "" {
		return "(an unreadable url)"
	}

	return u.Scheme + "://" + u.Host
}

// webhookFailed reports whether a push would fail to deploy.
//
// Only a recorded failure and a drifted hook count. Pending means GitHub has
// recorded nothing either way, and Unknown means the check could not run at all
// — treating either as broken would report the check's own latency, or a token
// without a scope, as a broken webhook.
func webhookFailed(r github.Reachability, drift []string) bool {
	return r.State == github.Failed || len(drift) > 0
}

// webhookExit is the exit code a reachability report ends on. It is shared by
// `repo add`, `repo rotate` and `repo check`: all three answer the same
// question, and all three must let a script act on the answer.
//
// It applies only where an exit code is read. In a terminal the report is the
// result and the operator already has the fix on screen; returning an error
// there would only send the caller's menu back over a screen that has already
// answered, offering commands that now refuse themselves.
func webhookExit(r github.Reachability, drift []string) error {
	if webhookFailed(r, drift) && (flagJSON || !isInteractive()) {
		return errWebhookUnreachable
	}

	return nil
}

// repairHook rewrites GitHub's copy of this server's webhook to what the server
// would register today: its own URL, the secret already in the store, and every
// event it needs. The deploy key and the HMAC secret are not rolled — that is
// `repo rotate`'s job, and rolling them to add a word to an event list is a
// blast radius nobody asked for.
//
// It writes to GitHub, so it confirms in a terminal and demands --yes anywhere
// else.
func repairHook(ctx context.Context, client *github.Client, repo store.Repo, want string, drift []string) error {
	if !flagYes {
		if !isInteractive() {
			return fmt.Errorf("repairing the webhook of %s rewrites it on github — re-run with `--yes`", repo.Repository)
		}

		ok, err := ui.Confirm("Rewrite the webhook of "+repo.Repository+" on github?", "\n  "+strings.Join(drift, "\n  "))
		if err != nil {
			return err
		}
		if !ok {
			return ui.ErrBack
		}
	}

	if err := ui.Spinner("Rewriting the webhook on GitHub…", func() error {
		return client.UpdateHook(ctx, repo.Repository, repo.GitHubHookID, want, repo.Secret)
	}); err != nil {
		return err
	}

	ui.Success("github's copy of the webhook now matches this server")

	return nil
}
