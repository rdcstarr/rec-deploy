package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/ui"
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

// TestIsCleanExitCoversTheCompletionSignal guards the reason ui.ErrDone exists.
// A command dispatched from a menu that ran to completion finished the
// operator's request; if that signal were not a clean exit it would surface as a
// red "error:" line under successful output — and, for a command launched
// directly, would make rec-deploy exit non-zero after doing exactly what was
// asked.
func TestIsCleanExitCoversTheCompletionSignal(t *testing.T) {
	for _, err := range []error{nil, ui.ErrBack, ui.ErrQuit, ui.ErrDone} {
		if !isCleanExit(err) {
			t.Errorf("isCleanExit(%v) = false, want a clean exit", err)
		}
	}
	if isCleanExit(errors.New("github token is not configured")) {
		t.Error("a real failure was treated as a clean exit and would never reach the operator")
	}
	if !isCleanExit(fmt.Errorf("wrapping: %w", ui.ErrDone)) {
		t.Error("a wrapped completion signal was not recognised")
	}
}

// TestDispatchTurnsCompletionIntoErrDone pins the rule behind exiting the TUI
// after a command finishes. A leaf chosen from a menu that runs cleanly must
// come back as ui.ErrDone so the menu unwinds to the shell instead of redrawing
// over the command's output; a real failure must pass through unchanged.
func TestDispatchTurnsCompletionIntoErrDone(t *testing.T) {
	root := &cobra.Command{Use: "rec-deploy", SilenceErrors: true, SilenceUsage: true}

	ran := false
	root.AddCommand(&cobra.Command{
		Use:  "act",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { ran = true; return nil },
	})
	boom := errors.New("it broke")
	root.AddCommand(&cobra.Command{
		Use:  "fail",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return boom },
	})

	if err := dispatch(root, "act"); !errors.Is(err, ui.ErrDone) {
		t.Errorf("dispatch of a clean command = %v, want ui.ErrDone", err)
	}
	if !ran {
		t.Error("dispatch did not actually run the command")
	}
	if err := dispatch(root, "fail"); !errors.Is(err, boom) {
		t.Errorf("dispatch of a failing command = %v, want the real error to reach the operator", err)
	}
}
