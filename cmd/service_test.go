package cmd

import (
	"context"
	"strings"
	"testing"
)

// TestLifecycleOptionsOffersExactlyTheApplicableTransition pins the invariant
// the service menu rests on: only the transition that applies is offered, so a
// running daemon is never offered a start and a stopped one never a stop.
// Driving it off the real systemd.Available() would skip the whole branch on
// any host without systemd — including the box this was written on — and pass
// vacuously even if the condition were reversed. Passing an explicit
// daemonLifecycle exercises all three states regardless of the host.
func TestLifecycleOptionsOffersExactlyTheApplicableTransition(t *testing.T) {
	tests := []struct {
		name  string
		state daemonLifecycle
		want  []string
	}{
		{"no systemd offers no lifecycle action", daemonUnmanaged, nil},
		{"active offers restart and stop, never start", daemonActive, []string{"restart", "stop"}},
		{"inactive offers start, never stop or restart", daemonInactive, []string{"start"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := make(map[string]bool)
			for _, option := range lifecycleOptions(tt.state) {
				seen[option.Value] = true
			}

			if seen["start"] && seen["stop"] {
				t.Fatalf("lifecycleOptions(%v) offers both start and stop: %v", tt.state, seen)
			}
			for _, want := range tt.want {
				if !seen[want] {
					t.Errorf("lifecycleOptions(%v) missing %q: %v", tt.state, want, seen)
				}
			}
			if len(seen) != len(tt.want) {
				t.Errorf("lifecycleOptions(%v) = %v, want exactly %v", tt.state, seen, tt.want)
			}
		})
	}
}

// TestEveryLifecycleOptionIsASubcommand is what keeps the menu and the command
// tree from drifting: the menu dispatches its choice through cobra, so an
// option whose value names no subcommand of `service` is a dead entry that
// fails only when an operator picks it.
func TestEveryLifecycleOptionIsASubcommand(t *testing.T) {
	children := make(map[string]bool)
	for _, c := range newServiceCmd().Commands() {
		children[c.Name()] = true
	}

	for _, state := range []daemonLifecycle{daemonUnmanaged, daemonActive, daemonInactive} {
		for _, option := range lifecycleOptions(state) {
			if !children[option.Value] {
				t.Errorf("lifecycleOptions(%v) offers %q, which is no subcommand of `service`", state, option.Value)
			}
		}
	}
}

// TestDestructiveServiceActionsNeedYesWithoutATerminal pins the --yes contract.
// Stopping or restarting cuts a running deploy short, so with no terminal to
// confirm in the action must refuse and name the flag — never act silently.
// The error is returned before systemd is ever reached, which is what makes
// this safe to run on a host that has the unit installed.
func TestDestructiveServiceActionsNeedYesWithoutATerminal(t *testing.T) {
	saved := flagYes
	defer func() { flagYes = saved }()
	flagYes = false

	// The test binary's stdin is not a terminal, so isInteractive() is false.
	for _, action := range []string{"stop", "restart"} {
		err := serviceAction(context.Background(), action)
		if err == nil {
			t.Fatalf("serviceAction(%q) without --yes on a non-terminal returned no error", action)
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("serviceAction(%q) error %q does not name the flag that unblocks it", action, err)
		}
	}
}

// TestUnknownServiceActionIsRejected guards the switch's default arm: a value
// that reached serviceAction without matching a verb must fail loudly rather
// than fall through to a spinner with a nil command.
func TestUnknownServiceActionIsRejected(t *testing.T) {
	saved := flagYes
	defer func() { flagYes = saved }()
	flagYes = true // skip the confirmation and reach the switch

	if err := serviceAction(context.Background(), "reload"); err == nil {
		t.Error("serviceAction(\"reload\") returned no error")
	}
}
