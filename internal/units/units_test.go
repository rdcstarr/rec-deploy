package units

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Names drives what status inspects and what the installer writes. A unit added
// to files/ but not to Names is invisible to both — it ships in the release and
// no server ever installs it, with nothing anywhere saying so.
func TestNamesCoversEveryEmbeddedUnit(t *testing.T) {
	embedded := All()
	if len(embedded) == 0 {
		t.Fatal("no units embedded — every test below would pass vacuously")
	}

	for _, name := range embedded {
		if !slices.Contains(Names, name) {
			t.Errorf("%s is embedded but missing from Names — nothing installs or checks it", name)
		}
	}
	for _, name := range Names {
		if !slices.Contains(embedded, name) {
			t.Errorf("Names has %s but files/ does not — status would report it missing on every box", name)
		}
	}
}

// The units the binary carries must be the units the release ships. Content is
// what every comparison is made against, so an empty or truncated read would
// report every box as drifted.
func TestContentReturnsTheUnit(t *testing.T) {
	got, err := Content("rec-deploy.service")
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("rec-deploy.service is empty")
	}

	if _, err := Content("not-a-unit.service"); err == nil {
		t.Error("Content invented a unit that is not embedded")
	}
}

func TestCompare(t *testing.T) {
	want, err := Content("rec-deploy.service")
	if err != nil {
		t.Fatalf("Content: %v", err)
	}

	dir := t.TempDir()
	same := filepath.Join(dir, "same.service")
	if err := os.WriteFile(same, want, 0o644); err != nil {
		t.Fatal(err)
	}
	// The drift that actually happened: a box installed at v0.1.0 and self-updated
	// since runs a unit with no TimeoutStopSec, so systemd SIGKILLs the daemon at
	// 90s while it drains — which strands deploy rows and breaks rollback.
	old := filepath.Join(dir, "old.service")
	if err := os.WriteFile(old, []byte("[Unit]\nDescription=an older release's unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		path string
		want State
	}{
		{"identical", same, StateCurrent},
		{"drifted", old, StateStale},
		{"systemd resolved nothing", "", StateMissing},
		{"path systemd named is gone", filepath.Join(dir, "absent.service"), StateUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Compare("rec-deploy.service", tc.path)
			if got.State != tc.want {
				t.Errorf("State = %q, want %q (detail: %s)", got.State, tc.want, got.Detail)
			}
			if got.Unit != "rec-deploy.service" {
				t.Errorf("Unit = %q", got.Unit)
			}
		})
	}
}

// A unit that ships without TimeoutStopSec is the one drift that has already
// bitten: the daemon drains for up to 15 minutes and systemd's default patience
// is 90 seconds, so it gets SIGKILLed mid-deploy — which is exactly the hard kill
// that strands a running row and kills rollback.
func TestTheServiceUnitStillBoundsItsStop(t *testing.T) {
	got, err := Content("rec-deploy.service")
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if !strings.Contains(string(got), "TimeoutStopSec=960") {
		t.Errorf("rec-deploy.service no longer sets TimeoutStopSec=960 — systemd would SIGKILL the drain at 90s")
	}
}

func TestMCPServiceAllowsSQLiteCoordinationFiles(t *testing.T) {
	got, err := Content("rec-deploy-mcp.service")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "ProtectSystem=strict") ||
		!strings.Contains(string(got), "ReadWritePaths=/var/lib/rec-deploy") {
		t.Error("MCP unit cannot create SQLite WAL/SHM coordination files")
	}
}
