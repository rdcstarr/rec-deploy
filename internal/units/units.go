// Package units carries the systemd units this binary ships, so it can tell
// whether the ones systemd is running are the ones it expects.
//
// rec-deploy ships three artifacts that have to agree: the binary, the systemd
// units and the SQLite schema. The schema reconciles itself on every open, and
// the binary replaces itself; the units were the leftover. self-update rewrites
// only the binary, so every update widens the gap between what the binary assumes
// and what the unit on disk says — silently, forever, with auto-update making it
// monotonic. It has bitten once already: a box installed at v0.1.0 and updated
// since runs without TimeoutStopSec, so systemd SIGKILLs the daemon at 90s while
// it drains, which strands deploy rows and breaks rollback.
//
// This package does not repair anything. Drift is reported and the operator
// re-runs install.sh, which takes the units from the same verified tag as the
// binary. Writing units from inside self-update is the one repair that can brick
// a box: if a new unit is why the daemon will not start, rollback restores only
// the binary — leaving the old binary under the broken unit, unattended, with the
// bad tag recorded so no later release can heal it.
package units

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
)

//go:embed files/*
var files embed.FS

// Names are the units rec-deploy installs, in the order install.sh writes them.
var Names = []string{
	"rec-deploy.service",
	"rec-deploy-mcp.service",
	"rec-deploy-mcp-tunnel.service",
	"rec-deploy-mcp-update.service",
	"rec-deploy-mcp-update.timer",
	"rec-deploy-update.service",
	"rec-deploy-update.timer",
}

// State is what happened to one unit when it was compared against the copy this
// binary carries.
type State string

// The states a unit can be found in.
const (
	// StateCurrent means the file on disk is byte-identical to the embedded one.
	StateCurrent State = "current"
	// StateStale means systemd is running a different version of this unit.
	StateStale State = "stale"
	// StateMissing means systemd cannot find the unit at all.
	StateMissing State = "missing"
	// StateMasked means the unit is symlinked to /dev/null. Nothing about the
	// shipped copy matters while it is: the mask wins over any reinstall.
	StateMasked State = "masked"
	// StateUnreadable means systemd named a path this process could not read.
	StateUnreadable State = "unreadable"
)

// Status is one unit's verdict, with the path systemd resolved for it. The json
// tags let `rec-deploy status --json` render the typed value directly, so the
// text and JSON paths share one shape.
type Status struct {
	Unit  string `json:"unit"`
	State State  `json:"state"`
	Path  string `json:"path,omitempty"`
	// Detail carries the read error behind StateUnreadable.
	Detail string `json:"detail,omitempty"`
}

// Content returns the unit exactly as this binary ships it.
func Content(name string) ([]byte, error) {
	b, err := files.ReadFile("files/" + name)
	if err != nil {
		return nil, fmt.Errorf("no embedded unit %q: %w", name, err)
	}

	return b, nil
}

// Compare reports how the unit at path stands against the embedded copy.
//
// An empty path means systemd resolved none. Pass the path systemd itself named,
// never one inferred from a directory or from package ownership: /etc shadows
// /lib, so a box whose units came from two installers runs the copy systemd
// picked, and a rule that guesses would compare the file nobody runs and report
// no drift.
func Compare(name, path string) Status {
	s := Status{Unit: name, Path: path}
	if path == "" {
		s.State = StateMissing

		return s
	}

	want, err := Content(name)
	if err != nil {
		s.State, s.Detail = StateUnreadable, err.Error()

		return s
	}

	got, err := os.ReadFile(path)
	if err != nil {
		s.State, s.Detail = StateUnreadable, err.Error()

		return s
	}

	s.State = StateStale
	if bytes.Equal(want, got) {
		s.State = StateCurrent
	}

	return s
}

// All returns the embedded unit names actually present, which Names must match.
// It exists so a unit added to files/ without being added to Names is caught by a
// test rather than by a server that never installs it.
func All() []string {
	entries, err := fs.ReadDir(files, "files")
	if err != nil {
		return nil
	}

	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}

	return out
}
