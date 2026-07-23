// Package uninstall removes rec-deploy from the local system: services, unit
// files, data directories and the binary itself. The GitHub-side cleanup
// deliberately lives in cmd, where the repo administration code already is —
// this engine never talks to the network.
package uninstall

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rdcstarr/rec-deploy/internal/systemd"
)

// Options names every path the engine may touch, so tests point it at a
// scratch directory and the command at the real system.
type Options struct {
	// UnitsDir is where the unit files live for a non-package install
	// (/etc/systemd/system). A .deb/.rpm install drops its own copies in
	// /lib/systemd/system, which this field never points at and which the package
	// manager removes on its own; unitFiles asks about each file here in its own
	// right, since a package owning the binary says nothing about a unit under
	// /etc.
	UnitsDir string
	// Units are the unit file names the installer drops.
	Units []string
	// DataDirs are the directories holding config and state.
	DataDirs []string
	// BinaryPath is the executable to remove last.
	BinaryPath string
	// KeepData leaves DataDirs untouched.
	KeepData bool
}

// Outcome is what happened to one target.
type Outcome string

// The outcomes a step can report.
const (
	OutcomeRemoved  Outcome = "removed"
	OutcomeKept     Outcome = "kept"
	OutcomeSkipped  Outcome = "already gone"
	OutcomeFailed   Outcome = "failed"
	OutcomeDeferred Outcome = "left to the package manager"
)

// Step is one target's fate. Detail carries the error text or the hint.
type Step struct {
	Target  string  `json:"target"`
	Outcome Outcome `json:"outcome"`
	Detail  string  `json:"detail,omitempty"`
}

// Report is the full account of a run — nothing the engine did is silent.
type Report struct {
	// Package is the owning package's name when the binary was installed via
	// .deb/.rpm; its files are then deferred to the package manager.
	Package string `json:"package"`
	Steps   []Step `json:"steps"`
}

// Failed reports whether any attempted step actually failed. Kept, skipped
// and deferred targets are success.
func (r Report) Failed() bool {
	for _, s := range r.Steps {
		if s.Outcome == OutcomeFailed {
			return true
		}
	}

	return false
}

// Seams: every call that would touch the real system is swappable in tests.
var (
	defaultSystemdAvailable = systemd.Available
	defaultDisableNow       = systemd.DisableNow
	defaultReload           = systemd.Reload
	defaultPackageOwner     = queryPackageOwner
	defaultUnitEnabled      = systemd.IsEnabled
	defaultUnitActive       = systemd.IsActive

	systemdAvailable = defaultSystemdAvailable
	disableNow       = defaultDisableNow
	reload           = defaultReload
	packageOwner     = defaultPackageOwner
	unitEnabled      = defaultUnitEnabled
	unitActive       = defaultUnitActive
)

// disableUnits are the units that can be enabled and must therefore be
// stopped and disabled; rec-deploy-update.service is oneshot-only and has no
// enablement state of its own.
var disableUnits = []string{"rec-deploy.service", "rec-deploy-mcp.service", "rec-deploy-mcp-tunnel.service", "rec-deploy-mcp-update.timer", "rec-deploy-update.timer"}

// Run removes the local installation per opts and accounts for every target.
// It never aborts midway: a failed step is recorded and the remaining targets
// still get their chance — a half-removed install must be finishable by a
// second run.
func Run(ctx context.Context, opts Options) Report {
	var r Report
	r.Package = packageOwner(ctx, opts.BinaryPath)

	r.services(ctx, opts)
	r.unitFiles(ctx, opts)
	r.data(opts)
	r.binary(opts)

	return r
}

// services stops and disables the enablable units. It gates on systemd's own
// unit state — enabled at boot or active right now — never on whether a unit
// file sits under opts.UnitsDir: a .deb/.rpm install drops its unit files in
// /lib/systemd/system, which opts.UnitsDir (/etc/systemd/system) never sees,
// so a file-location gate reads that absence as "already gone" and leaves a
// running daemon in place. State, not location, is the truth.
func (r *Report) services(ctx context.Context, opts Options) {
	if !systemdAvailable() {
		r.Steps = append(r.Steps, Step{Target: "services", Outcome: OutcomeSkipped, Detail: "no systemd on this host"})
		return
	}

	for _, unit := range disableUnits {
		if !unitEnabled(ctx, unit) && !unitActive(ctx, unit) {
			r.Steps = append(r.Steps, Step{Target: unit, Outcome: OutcomeSkipped, Detail: "not enabled or running"})
			continue
		}
		if err := disableNow(ctx, unit); err != nil {
			r.Steps = append(r.Steps, Step{Target: unit, Outcome: OutcomeFailed, Detail: err.Error()})
			continue
		}
		r.Steps = append(r.Steps, Step{Target: unit, Outcome: OutcomeRemoved, Detail: "stopped and disabled"})
	}
}

// unitFiles removes the unit files and reloads systemd, deferring only the ones
// a package really owns.
//
// Ownership is asked per file, not inherited from the binary. The two are
// independent: the package puts its units in /lib/systemd/system, while
// opts.UnitsDir is /etc/systemd/system, which no package owns. Gating on the
// binary's owner deferred every unit in UnitsDir to a package manager that had
// never heard of the file — a deferral to nobody, leaving the unit on disk while
// the report said it was handled. That is the report claiming a success it did
// not perform, which is the defect this tool exists to make impossible.
//
// A mixed install is the ordinary case that exposes it: a .deb for the binary and
// install.sh for the units, or the reverse.
func (r *Report) unitFiles(ctx context.Context, opts Options) {
	removed := false
	for _, unit := range opts.Units {
		path := filepath.Join(opts.UnitsDir, unit)
		if owner := packageOwner(ctx, path); owner != "" {
			r.Steps = append(r.Steps, Step{Target: path, Outcome: OutcomeDeferred, Detail: "owned by " + owner + " — removed by the package manager"})
			continue
		}
		switch err := os.Remove(path); {
		case err == nil:
			removed = true
			r.Steps = append(r.Steps, Step{Target: path, Outcome: OutcomeRemoved})
		case errors.Is(err, os.ErrNotExist):
			r.Steps = append(r.Steps, Step{Target: path, Outcome: OutcomeSkipped})
		default:
			r.Steps = append(r.Steps, Step{Target: path, Outcome: OutcomeFailed, Detail: err.Error()})
		}
	}

	if removed && systemdAvailable() {
		if err := reload(ctx); err != nil {
			r.Steps = append(r.Steps, Step{Target: "daemon-reload", Outcome: OutcomeFailed, Detail: err.Error()})
		}
	}
}

// data removes (or keeps) the config and state directories — the token, the
// HMAC secrets, the deploy keys and the database live here.
func (r *Report) data(opts Options) {
	for _, dir := range opts.DataDirs {
		if opts.KeepData {
			r.Steps = append(r.Steps, Step{Target: dir, Outcome: OutcomeKept})
			continue
		}
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			r.Steps = append(r.Steps, Step{Target: dir, Outcome: OutcomeSkipped})
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			r.Steps = append(r.Steps, Step{Target: dir, Outcome: OutcomeFailed, Detail: err.Error()})
			continue
		}
		r.Steps = append(r.Steps, Step{Target: dir, Outcome: OutcomeRemoved})
	}
}

// binary removes the executable last, so everything above ran from a binary
// that still existed. Package-owned binaries are the package manager's job.
func (r *Report) binary(opts Options) {
	if r.Package != "" {
		r.Steps = append(r.Steps, Step{
			Target:  opts.BinaryPath,
			Outcome: OutcomeDeferred,
			Detail:  "finish with:  dpkg -r " + r.Package + "  (or `rpm -e`)",
		})
		return
	}

	switch err := os.Remove(opts.BinaryPath); {
	case err == nil:
		r.Steps = append(r.Steps, Step{Target: opts.BinaryPath, Outcome: OutcomeRemoved})
	case errors.Is(err, os.ErrNotExist):
		r.Steps = append(r.Steps, Step{Target: opts.BinaryPath, Outcome: OutcomeSkipped})
	default:
		r.Steps = append(r.Steps, Step{Target: opts.BinaryPath, Outcome: OutcomeFailed, Detail: err.Error()})
	}
}

// queryPackageOwner asks dpkg and rpm whether path belongs to a package and
// returns the package name, or "" for a tarball/script install. Both tools
// exit non-zero for an unowned path, which is an answer, not an error.
func queryPackageOwner(ctx context.Context, path string) string {
	if out, err := exec.CommandContext(ctx, "dpkg", "-S", path).Output(); err == nil {
		if name, _, ok := strings.Cut(strings.TrimSpace(string(out)), ":"); ok {
			return name
		}
	}
	if out, err := exec.CommandContext(ctx, "rpm", "-qf", "--qf", "%{NAME}", path).Output(); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" && !strings.Contains(name, " ") {
			return name
		}
	}

	return ""
}
