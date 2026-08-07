package cmd

import (
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/github"
)

func TestDispatchReachCountsSubscribedHooks(t *testing.T) {
	hooks := []github.Hook{
		{ID: 1, Active: true, Events: []string{"push", "repository_dispatch"}},
		{ID: 2, Active: true, Events: []string{"push"}},
		{ID: 3, Active: false, Events: []string{"push", "repository_dispatch"}},
	}

	ready, stale := dispatchReach(hooks)
	if ready != 1 {
		t.Errorf("ready = %d, want 1 — an inactive hook delivers nothing", ready)
	}
	if stale != 2 {
		t.Errorf("stale = %d, want 2", stale)
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
