package uninstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scaffold lays out a fake install under t.TempDir() and stubs every seam so
// no test can touch the real system.
func scaffold(t *testing.T) (opts Options, calls *[]string) {
	t.Helper()
	dir := t.TempDir()

	unitsDir := filepath.Join(dir, "units")
	dataA := filepath.Join(dir, "etc")
	dataB := filepath.Join(dir, "lib")
	bin := filepath.Join(dir, "rec-deploy")
	for _, d := range []string{unitsDir, dataA, dataB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	units := []string{"rec-deploy.service", "rec-deploy-mcp.service", "rec-deploy-mcp-tunnel.service", "rec-deploy-mcp-update.service", "rec-deploy-mcp-update.timer", "rec-deploy-update.service", "rec-deploy-update.timer"}
	for _, u := range units {
		if err := os.WriteFile(filepath.Join(unitsDir, u), []byte("[Unit]"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dataA, "config.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("ELF"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := []string{}
	systemdAvailable = func() bool { return true }
	unitEnabled = func(_ context.Context, _ string) bool { return true }
	unitActive = func(_ context.Context, _ string) bool { return true }
	disableNow = func(_ context.Context, unit string) error { rec = append(rec, "disable "+unit); return nil }
	reload = func(_ context.Context) error { rec = append(rec, "reload"); return nil }
	packageOwner = func(_ context.Context, _ string) string { return "" }
	t.Cleanup(func() {
		systemdAvailable = defaultSystemdAvailable
		unitEnabled = defaultUnitEnabled
		unitActive = defaultUnitActive
		disableNow = defaultDisableNow
		reload = defaultReload
		packageOwner = defaultPackageOwner
	})

	return Options{
		UnitsDir:   unitsDir,
		Units:      units,
		DataDirs:   []string{dataA, dataB},
		BinaryPath: bin,
	}, &rec
}

func outcomes(r Report) map[string]Outcome {
	m := map[string]Outcome{}
	for _, s := range r.Steps {
		m[s.Target] = s.Outcome
	}
	return m
}

// TestRunRemovesEverything is the happy path: services disabled, unit files
// and data gone, binary gone, nothing failed.
func TestRunRemovesEverything(t *testing.T) {
	opts, calls := scaffold(t)

	r := Run(context.Background(), opts)

	if r.Failed() {
		t.Fatalf("run failed: %+v", r.Steps)
	}
	want := []string{"disable rec-deploy.service", "disable rec-deploy-mcp.service", "disable rec-deploy-mcp-tunnel.service", "disable rec-deploy-mcp-update.timer", "disable rec-deploy-update.timer", "reload"}
	if len(*calls) != len(want) {
		t.Fatalf("systemd calls = %v, want %v", *calls, want)
	}
	for i, w := range want {
		if (*calls)[i] != w {
			t.Errorf("call %d = %q, want %q", i, (*calls)[i], w)
		}
	}
	for _, p := range append([]string{opts.BinaryPath}, opts.DataDirs...) {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists", p)
		}
	}
	if _, err := os.Stat(filepath.Join(opts.UnitsDir, "rec-deploy.service")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unit file still exists")
	}
}

// TestRunKeepData leaves the data directories and reports them kept.
func TestRunKeepData(t *testing.T) {
	opts, _ := scaffold(t)
	opts.KeepData = true

	r := Run(context.Background(), opts)

	if r.Failed() {
		t.Fatalf("run failed: %+v", r.Steps)
	}
	for _, d := range opts.DataDirs {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s was removed despite KeepData", d)
		}
		if outcomes(r)[d] != OutcomeKept {
			t.Errorf("outcome for %s = %v, want kept", d, outcomes(r)[d])
		}
	}
}

// TestRunPackageOwnedDefersBinary leaves the package's binary to the package
// manager but still removes the units under UnitsDir and the data.
//
// The mixed install is the ordinary case: a .deb for the binary, install.sh for
// the units. The package owns /usr/bin/rec-deploy and knows nothing about
// /etc/systemd/system — so deferring the unit there defers it to nobody, and the
// report says "handled" about a file still on disk.
func TestRunPackageOwnedDefersBinary(t *testing.T) {
	opts, calls := scaffold(t)
	// The fake honours its path, as dpkg does: only the binary is owned.
	packageOwner = func(_ context.Context, path string) string {
		if path == opts.BinaryPath {
			return "rec-deploy"
		}

		return ""
	}

	r := Run(context.Background(), opts)

	if r.Package != "rec-deploy" {
		t.Fatalf("Package = %q", r.Package)
	}
	if _, err := os.Stat(opts.BinaryPath); err != nil {
		t.Errorf("binary was removed despite package ownership")
	}
	if outcomes(r)[opts.BinaryPath] != OutcomeDeferred {
		t.Errorf("binary outcome = %v, want deferred", outcomes(r)[opts.BinaryPath])
	}

	unit := filepath.Join(opts.UnitsDir, "rec-deploy.service")
	if _, err := os.Stat(unit); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the unit in UnitsDir survived — it was deferred to a package that does not own it")
	}
	if outcomes(r)[unit] != OutcomeRemoved {
		t.Errorf("unit outcome = %v, want removed", outcomes(r)[unit])
	}

	for _, d := range opts.DataDirs {
		if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("data %s should still be removed", d)
		}
	}
	// services still get stopped — the operator asked to uninstall.
	if len(*calls) == 0 {
		t.Errorf("services were not disabled")
	}
}

// A unit the package really does own is left alone: removing a path the package
// manager tracks either does nothing or races it.
func TestRunDefersAUnitThePackageOwns(t *testing.T) {
	opts, _ := scaffold(t)
	packageOwner = func(_ context.Context, _ string) string { return "rec-deploy" }

	r := Run(context.Background(), opts)

	unit := filepath.Join(opts.UnitsDir, "rec-deploy.service")
	if _, err := os.Stat(unit); err != nil {
		t.Errorf("a package-owned unit was removed: %v", err)
	}
	if outcomes(r)[unit] != OutcomeDeferred {
		t.Errorf("unit outcome = %v, want deferred", outcomes(r)[unit])
	}
}

// TestRunIdempotentOverHalfRemovedInstall reports already-gone pieces as
// skipped and still succeeds.
func TestRunIdempotentOverHalfRemovedInstall(t *testing.T) {
	opts, _ := scaffold(t)
	if err := os.Remove(opts.BinaryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(opts.UnitsDir, "rec-deploy.service")); err != nil {
		t.Fatal(err)
	}

	r := Run(context.Background(), opts)

	if r.Failed() {
		t.Fatalf("second-pass run failed: %+v", r.Steps)
	}
	if outcomes(r)[opts.BinaryPath] != OutcomeSkipped {
		t.Errorf("missing binary outcome = %v, want skipped", outcomes(r)[opts.BinaryPath])
	}
}

// TestRunFailedDisableIsReportedNotFatal records the failure and keeps going.
func TestRunFailedDisableIsReportedNotFatal(t *testing.T) {
	opts, _ := scaffold(t)
	disableNow = func(_ context.Context, unit string) error { return errors.New("boom") }

	r := Run(context.Background(), opts)

	if !r.Failed() {
		t.Fatalf("a failed disable must mark the report failed")
	}
	if _, err := os.Stat(opts.BinaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("later steps did not run after a failed disable")
	}
}

// TestRunPackageLayoutStillDisablesServices is the regression the review
// demanded: a .deb/.rpm install drops its unit files in /lib/systemd/system,
// which opts.UnitsDir (/etc/systemd/system) never sees. Gating the disable on
// a file under opts.UnitsDir reads that absence as "already gone" and leaves
// a running daemon in place; gating on systemd's own unit state does not.
func TestRunPackageLayoutStillDisablesServices(t *testing.T) {
	opts, calls := scaffold(t)
	packageOwner = func(_ context.Context, _ string) string { return "rec-deploy" }
	unitEnabled = func(_ context.Context, _ string) bool { return true }
	for _, u := range opts.Units {
		if err := os.Remove(filepath.Join(opts.UnitsDir, u)); err != nil {
			t.Fatal(err)
		}
	}

	r := Run(context.Background(), opts)

	for _, u := range disableUnits {
		want := "disable " + u
		found := false
		for _, c := range *calls {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("disable seam not invoked for %s despite its unit file missing from opts.UnitsDir: calls = %v", u, *calls)
		}
	}
	for _, u := range opts.Units {
		path := filepath.Join(opts.UnitsDir, u)
		if outcomes(r)[path] != OutcomeDeferred {
			t.Errorf("unit file outcome for %s = %v, want deferred", path, outcomes(r)[path])
		}
	}
}

// TestServicesTruthTable pins the AND in services()'s gate
// (!unitEnabled && !unitActive): a unit is left alone only when it is
// neither enabled nor active. Every other test in this file drives both
// seams to the same value, which would still pass if the AND were swapped
// for an OR — these three cases each pin one arm of the truth table on its
// own, on a fresh scaffold.
func TestServicesTruthTable(t *testing.T) {
	cases := []struct {
		name        string
		enabled     bool
		active      bool
		wantDisable bool
	}{
		{name: "enabled only", enabled: true, active: false, wantDisable: true},
		{name: "active only", enabled: false, active: true, wantDisable: true},
		{name: "neither", enabled: false, active: false, wantDisable: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, calls := scaffold(t)
			unitEnabled = func(_ context.Context, _ string) bool { return tc.enabled }
			unitActive = func(_ context.Context, _ string) bool { return tc.active }

			r := Run(context.Background(), opts)

			for _, u := range disableUnits {
				want := "disable " + u
				found := false
				for _, c := range *calls {
					if c == want {
						found = true
						break
					}
				}
				if found != tc.wantDisable {
					t.Errorf("disable called for %s = %v, want %v (calls = %v)", u, found, tc.wantDisable, *calls)
				}

				if tc.wantDisable {
					continue
				}
				if outcomes(r)[u] != OutcomeSkipped {
					t.Errorf("outcome for %s = %v, want skipped", u, outcomes(r)[u])
				}
				var detail string
				for _, s := range r.Steps {
					if s.Target == u {
						detail = s.Detail
						break
					}
				}
				if detail != "not enabled or running" {
					t.Errorf("detail for %s = %q, want %q", u, detail, "not enabled or running")
				}
			}
		})
	}
}

// TestRunNoSystemd skips the service phase entirely.
func TestRunNoSystemd(t *testing.T) {
	opts, calls := scaffold(t)
	systemdAvailable = func() bool { return false }

	r := Run(context.Background(), opts)

	if r.Failed() {
		t.Fatalf("run failed: %+v", r.Steps)
	}
	if len(*calls) != 0 {
		t.Errorf("systemd was called on a host without systemd: %v", *calls)
	}
}

// TestEveryStepCarriesItsPhase is what keeps the report's grouping honest. A
// Step literal that forgets its Phase renders under a blank label on a real
// server and nowhere in any test that only reads Target and Outcome — so the
// field is checked here, where a forgotten one is a failure rather than a
// cosmetic surprise during an uninstall nobody re-runs.
func TestEveryStepCarriesItsPhase(t *testing.T) {
	opts, _ := scaffold(t)

	r := Run(context.Background(), opts)
	if len(r.Steps) == 0 {
		t.Fatal("Run recorded no steps")
	}

	for _, s := range r.Steps {
		if s.Phase == "" {
			t.Errorf("step %q has no phase", s.Target)
		}
	}
}

// TestPhasesAppearInRunOrderAndOnlyOnce pins the property reportLines depends
// on: a phase's steps are contiguous, so one label introduces all of them. If
// Run ever interleaved its passes, the report would repeat a label instead of
// grouping — noisier than the flat list the grouping replaced.
func TestPhasesAppearInRunOrderAndOnlyOnce(t *testing.T) {
	opts, _ := scaffold(t)

	var order []Phase
	for _, s := range Run(context.Background(), opts).Steps {
		if len(order) == 0 || order[len(order)-1] != s.Phase {
			order = append(order, s.Phase)
		}
	}

	want := []Phase{PhaseServices, PhaseUnitFiles, PhaseData, PhaseBinary}
	if len(order) != len(want) {
		t.Fatalf("phase runs = %v, want %v — a repeated phase means the passes interleaved", order, want)
	}
	for i, w := range want {
		if order[i] != w {
			t.Errorf("phase run %d = %q, want %q", i, order[i], w)
		}
	}
}

// TestPhaseIsNotSerialized pins that grouping a human report did not change the
// document automation reads. --json is a contract; how the terminal lays the
// same facts out is not part of it.
func TestPhaseIsNotSerialized(t *testing.T) {
	opts, _ := scaffold(t)

	out, err := json.Marshal(Run(context.Background(), opts))
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	if strings.Contains(string(out), "phase") {
		t.Errorf("--json carries the phase: %s", out)
	}
	for _, want := range []string{`"target"`, `"outcome"`, `"package"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("--json lost %s: %s", want, out)
		}
	}
}
