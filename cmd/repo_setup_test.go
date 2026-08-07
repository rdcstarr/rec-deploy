package cmd

import (
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/github"
)

func TestDispatchReachCountsSubscribedHooks(t *testing.T) {
	hooks := []github.Hook{
		{ID: 1, Active: true, Events: []string{"push", "repository_dispatch"}, URL: "http://1.2.3.4:9000/hook/a"},
		{ID: 2, Active: true, Events: []string{"push"}, URL: "http://5.6.7.8:9000/hook/b"},
		{ID: 3, Active: false, Events: []string{"push", "repository_dispatch"}, URL: "http://9.9.9.9:9000/hook/c"},
	}

	ready, stale := dispatchReach(hooks)
	if ready != 1 {
		t.Errorf("ready = %d, want 1 — an inactive hook delivers nothing", ready)
	}
	if stale != 2 {
		t.Errorf("stale = %d, want 2", stale)
	}
}

// TestDispatchReachCountsOnlyRecDeployWebhooks is the guard the reachability
// check exists for. A repository that no rec-deploy server is registered on can
// still carry webhooks — a Slack app, a CI endpoint, a hook subscribed to
// everything with ["*"] — and counting those as servers turns the dispatch into
// a success report for work that will happen nowhere. They are also not
// repairable: there is no rec-deploy server behind a Slack hook to run
// `repo check --repair` on.
func TestDispatchReachCountsOnlyRecDeployWebhooks(t *testing.T) {
	hooks := []github.Hook{
		{ID: 1, Active: true, Events: []string{"push", "repository_dispatch"}, URL: "https://hooks.slack.com/services/T00/B00/xyz"},
		{ID: 2, Active: true, Events: []string{"*"}, URL: "https://ci.example.com/github"},
		{ID: 3, Active: true, Events: []string{"push"}, URL: "https://example.com/hooks/github"},
	}

	if ready, stale := dispatchReach(hooks); ready != 0 || stale != 0 {
		t.Errorf("reach = %d/%d over foreign webhooks alone, want 0/0", ready, stale)
	}
}

// TestDispatchReachRecognisesARecDeployWebhookUnderAPath covers a public_url
// with a base path — https://host/rec-deploy — which github.HookURL appends
// /hook/<token> to like any other.
func TestDispatchReachRecognisesARecDeployWebhookUnderAPath(t *testing.T) {
	hooks := []github.Hook{
		{ID: 1, Active: true, Events: []string{"push", "repository_dispatch"}, URL: "https://example.com/rec-deploy/hook/tok"},
		{ID: 2, Active: true, Events: []string{"*"}, URL: "http://5.6.7.8:9000/hook/tok2"},
	}

	if ready, stale := dispatchReach(hooks); ready != 2 || stale != 0 {
		t.Errorf("reach = %d/%d, want 2/0", ready, stale)
	}
}

func TestDispatchReachOnNoHooks(t *testing.T) {
	if ready, stale := dispatchReach(nil); ready != 0 || stale != 0 {
		t.Errorf("reach = %d/%d, want 0/0", ready, stale)
	}
}

func TestSetupCmdFlags(t *testing.T) {
	cmd := newRepoSetupCmd()

	if cmd.Flags().Lookup("branch") == nil {
		t.Error("--branch is not registered")
	}
	if !strings.Contains(cmd.Long, "every server") {
		t.Errorf("Long does not say the dispatch reaches every server: %q", cmd.Long)
	}
}

func TestRepoMenuOffersSetup(t *testing.T) {
	for _, o := range repoMenuOptions() {
		if o.Value == "setup" {
			return
		}
	}
	t.Error("the repo hub does not offer setup")
}
