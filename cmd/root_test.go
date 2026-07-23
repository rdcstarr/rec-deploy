package cmd

import (
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/config"
)

// TestEveryCommandIsReachable is what keeps a hand-written hub from drifting: a
// command must appear in hubOptions() under some config state — the hub's
// contents differ between an uninitialized and an initialized server, so
// "reachable" means the union of both, not either alone — or land in one of
// the group menus the hub opens, or be marked non-interactive on purpose.
// Unreachable in every state is the failure mode a curated list has and a
// reflected one does not.
func TestEveryCommandIsReachable(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()

	// Reachable from a group menu rather than from the hub.
	nested := map[string]string{
		"scan":     "status",
		"rollback": "repo",
	}

	hub := make(map[string]bool)
	for _, state := range []*config.Config{{Initialized: true}, {}} {
		cfg = state
		for _, option := range hubOptions() {
			hub[option.Value] = true
		}
	}

	for _, c := range newRootCmd().Commands() {
		name := c.Name()
		switch {
		case c.Hidden, name == "help", name == "completion":
		case c.Annotations[annotationInteractive] == "false":
		case hub[name], nested[name] != "":
		default:
			t.Errorf("%q is in no menu — add it to hubEntries, to a group menu, or set %s=false on it", name, annotationInteractive)
		}
	}
}

// TestHubOffersSetupOnlyBeforeItIsDone pins the one state-dependent entry: init
// leads the hub on a fresh server and disappears once the wizard has completed.
// It also pins that hubOptions tolerates a nil cfg — the state before
// PersistentPreRunE has loaded one — the same way initialized() does.
func TestHubOffersSetupOnlyBeforeItIsDone(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()

	cfg = nil
	options := hubOptions()
	if len(options) == 0 || options[0].Value != "init" {
		t.Errorf("a server with no config loaded must be offered setup first, got %+v", options)
	}

	cfg = &config.Config{}
	options = hubOptions()
	if len(options) == 0 || options[0].Value != "init" {
		t.Errorf("an uninitialized server must be offered setup first, got %+v", options)
	}

	cfg = &config.Config{Initialized: true}
	for _, option := range hubOptions() {
		if option.Value == "init" {
			t.Error("an initialized server is still offered setup")
		}
	}
}

// TestHubOmitsPlumbingAndTheDaemon pins that the hub lists operator commands
// only: cobra's own plumbing and the process systemd runs are not choices.
// uninstall is deliberately present — its root check and confirmation wizard are
// the guard, not its absence from the menu.
func TestHubOmitsPlumbingAndTheDaemon(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()
	cfg = &config.Config{Initialized: true}

	seen := make(map[string]bool)
	for _, option := range hubOptions() {
		seen[option.Value] = true
	}

	for _, unwanted := range []string{"help", "completion", "serve", "version"} {
		if seen[unwanted] {
			t.Errorf("hub lists %q", unwanted)
		}
	}
	if !seen["uninstall"] {
		t.Error("hub is missing the uninstall command")
	}
	if !seen["deploy"] {
		t.Error("hub is missing the deploy command")
	}
}
