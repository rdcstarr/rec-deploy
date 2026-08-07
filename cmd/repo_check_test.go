package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/store"
)

// TestRepairHookWritesNoProseOnStdoutForJSON pins the global contract: with
// --json, stdout is one document and nothing else. repairHook runs from inside
// checkRepo's --json branch, so a receipt line printed here lands above the
// document and `rec-deploy repo check o/r --repair --json | jq` fails to parse
// what is otherwise a successful repair.
func TestRepairHookWritesNoProseOnStdoutForJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := github.New("tok")
	client.BaseURL = srv.URL

	json, yes := flagJSON, flagYes
	flagJSON, flagYes = true, true
	defer func() { flagJSON, flagYes = json, yes }()

	var err error
	out := capture(t, func() {
		err = repairHook(context.Background(), client,
			store.Repo{Repository: "o/r", GitHubHookID: 7, Secret: "s3cret"},
			"http://1.2.3.4:9000/hook/tok", []string{"the webhook is deactivated on github"})
	})
	if err != nil {
		t.Fatalf("repairHook: %v", err)
	}
	if out != "" {
		t.Errorf("repairHook wrote %q to stdout, which --json reserves for the document", out)
	}
}

func TestHookDrift(t *testing.T) {
	const want = "http://1.2.3.4:9000/hook/abc"
	subscribed := []string{"push", "repository_dispatch"}

	if got := hookDrift(github.Hook{URL: want, Active: true, Events: subscribed}, want); len(got) != 0 {
		t.Errorf("hookDrift on a matching hook = %q, want none", got)
	}

	// Nothing re-points an existing hook when public_url changes, so GitHub keeps
	// delivering to the old address and no delivery ever reaches this server.
	// Both addresses are named: "they differ" is not something an operator can act on.
	got := hookDrift(github.Hook{URL: "http://5.6.7.8:9000/hook/abc", Active: true, Events: subscribed}, want)
	if len(got) != 1 {
		t.Fatalf("hookDrift on a moved hook = %q, want one issue", got)
	}
	if !strings.Contains(got[0], "5.6.7.8:9000") || !strings.Contains(got[0], "1.2.3.4:9000") {
		t.Errorf("issue = %q, want it to name both addresses", got[0])
	}

	if got := hookDrift(github.Hook{URL: want, Active: false, Events: subscribed}, want); len(got) != 1 {
		t.Errorf("hookDrift on a deactivated hook = %q, want one issue", got)
	}

	if got := hookDrift(github.Hook{URL: "http://5.6.7.8:9000/hook/abc", Events: subscribed}, want); len(got) != 2 {
		t.Errorf("hookDrift on a moved and deactivated hook = %q, want two issues", got)
	}
}

func TestHookDriftReportsAMissingDispatchSubscription(t *testing.T) {
	const want = "http://1.2.3.4:9000/hook/abc"

	got := hookDrift(github.Hook{URL: want, Active: true, Events: []string{"push"}}, want)
	if len(got) != 1 || !strings.Contains(got[0], "repository_dispatch") {
		t.Fatalf("drift = %v, want one line naming repository_dispatch", got)
	}
}

func TestHookDriftIsSilentWhenBothEventsAreSubscribed(t *testing.T) {
	const want = "http://1.2.3.4:9000/hook/abc"

	got := hookDrift(github.Hook{URL: want, Active: true, Events: []string{"push", "repository_dispatch"}}, want)
	if len(got) != 0 {
		t.Errorf("drift = %v, want none", got)
	}
}

// The contract this whole change exists for: a registration that wired up
// nothing must not read as a success. Equally, a check that could not answer
// must not read as a failure — that would trade a silent breakage for a noisy
// false alarm, and a script would learn to ignore both.
func TestWebhookFailed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state github.Reach
		drift []string
		want  bool
	}{
		{"github recorded a failure", github.Failed, nil, true},
		{"the hook points elsewhere", github.Reachable, []string{"github delivers to http://5.6.7.8:9000"}, true},
		{"delivered", github.Reachable, nil, false},
		// Nothing has been recorded yet. Saying "broken" would be a guess.
		{"not recorded yet", github.Pending, nil, false},
		// The token cannot read deliveries, or GitHub was unreachable from here.
		// An otherwise correct registration must not be failed by that.
		{"unanswerable", github.Unknown, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := webhookFailed(github.Reachability{State: tc.state}, tc.drift)
			if got != tc.want {
				t.Errorf("webhookFailed = %v, want %v", got, tc.want)
			}
		})
	}
}

// A hook deleted on GitHub by hand comes back as a zero value. That is one
// problem — it is gone — and the reachability verdict is what states it;
// comparing against nothing would invent a second and a third.
func TestHookDriftSaysNothingAboutAHookThatIsGone(t *testing.T) {
	if got := hookDrift(github.Hook{}, "http://1.2.3.4:9000/hook/abc"); len(got) != 0 {
		t.Errorf("hookDrift on a missing hook = %q, want none", got)
	}
}

// The URL carries the delivery token in its path. A report an operator may paste
// into an issue must not hand that token over.
func TestHookDriftRedactsTheDeliveryToken(t *testing.T) {
	got := hookDrift(
		github.Hook{URL: "http://5.6.7.8:9000/hook/s3cret-token-value", Active: true, Events: []string{"push", "repository_dispatch"}},
		"http://1.2.3.4:9000/hook/s3cret-token-value",
	)

	if len(got) != 1 {
		t.Fatalf("hookDrift = %q, want one issue", got)
	}
	if strings.Contains(got[0], "s3cret-token-value") {
		t.Errorf("issue = %q — it leaks the delivery token", got[0])
	}
}
