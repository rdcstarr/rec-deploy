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

func TestSelfUpdateInteractiveOptions(t *testing.T) {
	options := selfUpdateMenuOptions()
	if len(options) != 3 {
		t.Fatalf("self-update menu has %d options, want 3", len(options))
	}
	want := []string{"check", "install", "back"}
	for i, value := range want {
		if options[i].Value != value {
			t.Errorf("option %d = %q, want %q", i, options[i].Value, value)
		}
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
