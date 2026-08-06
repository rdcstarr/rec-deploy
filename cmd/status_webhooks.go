package cmd

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/store"
)

// deliveryProbeBudget bounds the whole batch of delivery reads. status answers
// "what needs attention?" and must stay fast: past this the webhooks are
// reported as unknown rather than holding the report hostage.
const deliveryProbeBudget = 5 * time.Second

// deliveryProbes is how many delivery reads run at once. Servers here hold a
// handful of repositories, and a wider fan-out would only buy rate limiting.
const deliveryProbes = 4

// webhookState is what status learned about one repository's deliveries.
type webhookState struct {
	Repository string
	Delivery   github.Delivery // the most recent delivery; zero if there is none
	Known      bool            // whether GitHub answered at all
}

// lastDeliveries reads the last delivery GitHub recorded for every registered
// repository.
//
// It is what catches a webhook that was good and stopped being good — a firewall
// rebuilt, an address changed, GitHub rotating its ranges — which no amount of
// checking at registration time can see. It never pings: status reports, it does
// not provoke.
//
// A repository whose deliveries cannot be read stays Known=false. Nothing here
// can fail the report: no token, no network and a slow GitHub all degrade to
// "unknown", because a failure to ask is not an answer.
func lastDeliveries(ctx context.Context, repos []store.Repo) []webhookState {
	if len(repos) == 0 {
		return nil
	}

	// The budget covers resolving the token too: with none configured, that shells
	// out to `gh auth token`, and status must not be able to hang on it.
	ctx, cancel := context.WithTimeout(ctx, deliveryProbeBudget)
	defer cancel()

	client, err := githubClient(ctx)
	if err != nil {
		return nil
	}

	out := make([]webhookState, len(repos))
	sem := make(chan struct{}, deliveryProbes)

	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			out[i] = webhookState{Repository: repo.Repository}

			got, err := client.Deliveries(ctx, repo.Repository, repo.GitHubHookID, 1)
			if err != nil {
				return
			}

			out[i].Known = true
			if len(got) > 0 {
				out[i].Delivery = got[0]
			}
		}()
	}
	wg.Wait()

	return out
}

// deliveryFlag describes a repository's last delivery for the status table, and
// returns the line it owes the "needs attention" block when a delivery failed.
func deliveryFlag(s webhookState) (flag, issue string) {
	switch {
	case !s.Known:
		return "· delivery unknown", ""
	case s.Delivery.ID == 0:
		return "· no delivery yet", ""
	case s.Delivery.OK():
		return "✓ delivering", ""
	}

	flag = fmt.Sprintf("✗ last delivery failed (%d %s)", s.Delivery.StatusCode, s.Delivery.Status)

	return flag, fmt.Sprintf("%s: github's last delivery failed (%d %s) — diagnose it with `rec-deploy repo check %s`",
		s.Repository, s.Delivery.StatusCode, s.Delivery.Status, s.Repository)
}

// webhookJSON renders the delivery states for --json, keyed by repository.
func webhookJSON(states []webhookState) map[string]any {
	out := make(map[string]any, len(states))
	for _, s := range states {
		if !s.Known {
			out[s.Repository] = map[string]any{"known": false}
			continue
		}

		entry := map[string]any{"known": true, "delivering": s.Delivery.OK()}
		if s.Delivery.ID != 0 {
			entry["status"] = s.Delivery.Status
			entry["status_code"] = s.Delivery.StatusCode
			entry["event"] = s.Delivery.Event
			entry["delivered_at"] = s.Delivery.DeliveredAt
		}
		out[s.Repository] = entry
	}

	return out
}
