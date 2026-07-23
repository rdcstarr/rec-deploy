package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/selfupdate"
	"github.com/rdcstarr/rec-deploy/internal/systemd"
)

// TestSelfUpdateRestartNeedsSystemd: --restart exists to be run by a systemd
// timer. Anywhere else it must say so and point at the flagless command, not
// half-update and leave the operator guessing.
func TestSelfUpdateRestartNeedsSystemd(t *testing.T) {
	if systemd.Available() {
		t.Skip("this host runs systemd; the no-systemd path cannot be exercised here")
	}

	err := selfUpdateRestart(context.Background(), "v0.1.0")
	if err == nil {
		t.Fatal("expected an error on a host without systemd")
	}
	if !strings.Contains(err.Error(), "systemd") {
		t.Errorf("the error must name systemd, got: %v", err)
	}
	if !strings.Contains(err.Error(), "rec-deploy self-update") {
		t.Errorf("the error must point at the command that does work, got: %v", err)
	}
}

// TestSelfUpdateHasARestartFlag guards the contract rec-deploy-update.service
// depends on: the unit's ExecStart is `rec-deploy self-update --restart`.
func TestSelfUpdateHasARestartFlag(t *testing.T) {
	if newSelfUpdateCmd().Flags().Lookup("restart") == nil {
		t.Fatal("self-update must have a --restart flag; rec-deploy-update.service calls it")
	}
}

// TestSelfUpdateHasNoSubMenu pins that self-update is one action. It used to
// open a three-entry menu whose second step was unconditional: check, then
// install, then back.
func TestSelfUpdateHasNoSubMenu(t *testing.T) {
	cmd := newSelfUpdateCmd()
	if len(cmd.Commands()) != 0 {
		t.Errorf("self-update grew subcommands: %v", cmd.Commands())
	}
	for _, flag := range []string{"check", "restart"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("self-update lost its --%s flag", flag)
		}
	}
}

// TestRunUpdatePathConfirmedRestartNeverInstalls pins the CRITICAL fix: a
// confirmed restart must run only the restart path, never the plain install
// first. The old interactive flow ran selfUpdateInstall unconditionally and
// then, on a confirmed restart, ApplyAndRestart a second time — which backed
// up the binary the first install had already replaced, so a rollback
// restored a copy of the new, possibly broken, release rather than the
// genuine outgoing one. This test fails if that install-then-restart
// sequence is reintroduced, because it would observe install having run.
func TestRunUpdatePathConfirmedRestartNeverInstalls(t *testing.T) {
	var installed, restarted bool

	err := runUpdatePath(true,
		func() error { installed = true; return nil },
		func() error { restarted = true; return nil },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if installed {
		t.Error("a confirmed restart must not also run the plain install path")
	}
	if !restarted {
		t.Error("a confirmed restart must run the restart path")
	}
}

// TestRunUpdatePathDeclinedRestartOnlyInstalls is the complementary case:
// without a confirmed restart, exactly the plain install runs.
func TestRunUpdatePathDeclinedRestartOnlyInstalls(t *testing.T) {
	var installed, restarted bool

	err := runUpdatePath(false,
		func() error { installed = true; return nil },
		func() error { restarted = true; return nil },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !installed {
		t.Error("a declined restart must run the plain install path")
	}
	if restarted {
		t.Error("a declined restart must not run the restart path")
	}
}

// TestSkipsKnownBadRelease: the updater skips only when the newest release is
// exactly the tag already recorded as bad. An empty memory never skips; a bad
// tag that is not the newest release (a good one has since superseded it) does
// not skip either.
func TestSkipsKnownBadRelease(t *testing.T) {
	cases := []struct {
		name   string
		badTag string
		chk    selfupdate.Result
		want   bool
	}{
		{"no memory", "", selfupdate.Result{Newer: true, Latest: "v0.2.0"}, false},
		{"latest is the bad tag", "v0.2.0", selfupdate.Result{Newer: true, Latest: "v0.2.0"}, true},
		{"a newer good release superseded it", "v0.2.0", selfupdate.Result{Newer: true, Latest: "v0.3.0"}, false},
		{"already up to date", "v0.2.0", selfupdate.Result{Newer: false, Latest: "v0.2.0"}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := skipsKnownBadRelease(c.badTag, c.chk); got != c.want {
				t.Errorf("skipsKnownBadRelease(%q, %+v) = %v, want %v", c.badTag, c.chk, got, c.want)
			}
		})
	}
}
