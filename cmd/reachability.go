package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/store"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// verifyBudget bounds the wait for GitHub to record a ping. Delivery is
// asynchronous and usually takes a second or two; past this the answer is "not
// yet", which `rec-deploy repo check` can ask again for.
const verifyBudget = 20 * time.Second

// errWebhookUnreachable is what a command exits with when GitHub cannot deliver
// to this server. The registration itself is intact and is never undone — the
// operator needs the key and the hook to stay while they open the firewall.
// The report above it carries the diagnosis; this line is what a script sees on
// stderr, so it still names where to get one.
var errWebhookUnreachable = errors.New("github cannot deliver to this server — push-to-deploy will do nothing until this is fixed; re-check it with `rec-deploy repo check <owner/repo>`")

// verifyWebhook asks GitHub whether it can reach this server, under a spinner and
// a bounded budget. It never returns an error: an unanswerable check is a verdict
// of its own.
func verifyWebhook(ctx context.Context, client *github.Client, repo store.Repo) github.Reachability {
	ctx, cancel := context.WithTimeout(ctx, verifyBudget)
	defer cancel()

	var got github.Reachability
	_ = ui.Spinner("Checking that GitHub can reach this server…", func() error {
		got = client.VerifyHook(ctx, repo.Repository, repo.GitHubHookID)

		return nil
	})

	return got
}

// webhookDiagnosis is what a verdict proves and what to do about it.
type webhookDiagnosis struct {
	Cause  string   // what is wrong, in one line
	Fix    []string // the steps that address it
	Ranges bool     // whether GitHub's delivery ranges belong under the fix
}

// diagnoseWebhook turns a verdict into a cause and a fix. The delivery says more
// than "it failed": GitHub records the code this server answered with, and the
// daemon answers a ping only after the HMAC check — so a 401 rules the network
// out, a 404 rules the address in, and a 502 means nothing answered at all.
func diagnoseWebhook(slug, endpoint string, r github.Reachability) webhookDiagnosis {
	switch r.State {
	case github.Reachable:
		return webhookDiagnosis{}

	case github.Pending:
		return webhookDiagnosis{
			Cause: "delivery is asynchronous and nothing has been recorded either way — this is not a verdict",
			Fix:   []string{"ask again with `rec-deploy repo check " + slug + "`"},
		}

	case github.Unknown:
		return webhookDiagnosis{Cause: r.Detail}
	}

	if r.Detail != "" {
		return webhookDiagnosis{
			Cause: r.Detail,
			Fix: []string{
				"re-register it with `rec-deploy repo remove " + slug + " --yes` then `rec-deploy repo add " + slug + "`",
			},
		}
	}

	d := r.Delivery
	switch {
	case unreachedCode(d):
		return webhookDiagnosis{
			Cause: fmt.Sprintf("github got no answer from %s (%s)", endpoint, d.Status),
			Fix: []string{
				"confirm the daemon is listening here with `rec-deploy status`",
				"allow inbound TCP to " + endpoint + " from github's delivery ranges",
			},
			Ranges: true,
		}

	case d.StatusCode == 401:
		return webhookDiagnosis{
			// The packet arrived and was rejected on its signature, so the network
			// is fine and only the shared secret is out of step.
			Cause: "github signed the delivery with a secret this server does not hold",
			Fix:   []string{"roll both sides together with `rec-deploy repo rotate " + slug + " --yes`"},
		}

	case d.StatusCode == 404:
		return webhookDiagnosis{
			Cause: "the webhook's url reaches this server but no repository is registered at that path",
			Fix: []string{
				"check that `public_url` still matches the address github was given: `rec-deploy repo config`",
				"compare the two with `rec-deploy repo check " + slug + "`",
			},
		}

	case d.StatusCode >= 500:
		return webhookDiagnosis{
			Cause: fmt.Sprintf("this server answered %d — the delivery arrived and failed here", d.StatusCode),
			Fix:   []string{"read what happened with `rec-deploy logs`"},
		}
	}

	return webhookDiagnosis{
		Cause: fmt.Sprintf("github recorded %q (HTTP %d)", d.Status, d.StatusCode),
		Fix:   []string{"ask again with `rec-deploy repo check " + slug + "`"},
	}
}

// unreachedCode reports whether the delivery never got an answer. GitHub
// synthesises a 502 when it cannot connect and records no code at all when it
// gives up first, and its status line names which of the two happened.
func unreachedCode(d github.Delivery) bool {
	if d.StatusCode == 0 || d.StatusCode == 502 || d.StatusCode == 504 {
		return true
	}

	s := strings.ToLower(d.Status)

	return strings.Contains(s, "failed to connect") || strings.Contains(s, "timed out")
}

// renderReachability prints the verdict, and under a bad one what it proves and
// how to fix it. A failure names the port and the ranges to allow rather than the
// symptom, because "webhook unreachable" only sends the operator hunting.
func renderReachability(ctx context.Context, slug, publicURL string, r github.Reachability) {
	if r.State == github.Reachable {
		ui.Success("github delivered a ping to this server — push-to-deploy works")

		return
	}

	d := diagnoseWebhook(slug, webhookEndpoint(publicURL), r)

	switch r.State {
	case github.Unknown:
		// The check could not run. Saying anything about the webhook here would be
		// a guess dressed as a verdict.
		ui.Info("could not check whether github can reach this server: " + d.Cause)

		return
	case github.Pending:
		ui.Warn("github has not recorded the ping yet")
	default:
		ui.Warn("github cannot deliver to " + slug + " on this server")
	}

	ui.KeyValue("cause", d.Cause)
	if len(d.Fix) > 0 {
		ui.KeyList("fix", d.Fix)
	}
	if d.Ranges {
		ui.KeyList("allow", deliveryRanges(ctx))
	}
}

// deliveryRanges lists the addresses GitHub delivers webhooks from, read live
// because GitHub rotates them — which is the whole reason to read /meta instead
// of shipping a constant. Failing to read them is not worth a second error on
// top of the one being reported, so it degrades to where they are published.
func deliveryRanges(ctx context.Context) []string {
	meta, err := github.FetchMeta(ctx)
	if err != nil || len(meta.Hooks) == 0 {
		return []string{"github's current ranges are published at " + github.MetaURL + " under \"hooks\""}
	}

	return meta.Hooks
}

// reachabilityJSON renders a verdict for --json.
func reachabilityJSON(r github.Reachability) map[string]any {
	out := map[string]any{"state": r.State.String(), "reachable": r.State == github.Reachable}
	if r.Detail != "" {
		out["detail"] = r.Detail
	}
	if r.Delivery.ID != 0 {
		out["delivery"] = map[string]any{
			"event":        r.Delivery.Event,
			"status":       r.Delivery.Status,
			"status_code":  r.Delivery.StatusCode,
			"delivered_at": r.Delivery.DeliveredAt,
		}
	}

	return out
}

// webhookEndpoint names the host and port GitHub connects to, so firewall advice
// can state a number instead of a hostname. A URL with no port still names the
// one its scheme implies.
func webhookEndpoint(publicURL string) string {
	u, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || u.Host == "" {
		return ""
	}

	if u.Port() != "" {
		return u.Host
	}

	port := "80"
	if u.Scheme == "https" {
		port = "443"
	}

	return net.JoinHostPort(u.Hostname(), port)
}
