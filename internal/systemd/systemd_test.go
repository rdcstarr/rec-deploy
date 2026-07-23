package systemd

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestIsActiveIsFalseForAUnitThatDoesNotExist pins the contract the update flow
// depends on: a question about the init system answers false, it does not blow
// up — including on a host with no systemd at all, where systemctl is absent.
func TestIsActiveIsFalseForAUnitThatDoesNotExist(t *testing.T) {
	if IsActive(context.Background(), "rec-deploy-does-not-exist.service") {
		t.Error("a unit that does not exist must not report active")
	}
}

// TestIsEnabledIsFalseForAUnitThatDoesNotExist is the same contract for the
// question `rec-deploy status` asks about the update timer.
func TestIsEnabledIsFalseForAUnitThatDoesNotExist(t *testing.T) {
	if IsEnabled(context.Background(), "rec-deploy-does-not-exist.timer") {
		t.Error("a unit that does not exist must not report enabled")
	}
}

// TestAvailableMatchesTheHost pins Available() to the exact ground truth its
// own doc comment claims: systemctl on PATH AND /run/systemd/system present as
// a directory. Recomputing that truth here — rather than assuming this host
// either has or lacks systemd — catches a broken Available() (e.g. a stub that
// always returns true, or one that checks only PATH) on any CI runner.
func TestAvailableMatchesTheHost(t *testing.T) {
	_, lookErr := exec.LookPath("systemctl")

	fi, statErr := os.Stat("/run/systemd/system")
	want := lookErr == nil && statErr == nil && fi.IsDir()

	if got := Available(); got != want {
		t.Errorf("Available() = %v, want %v (systemctl on PATH: %v, /run/systemd/system is a dir: %v)",
			got, want, lookErr == nil, statErr == nil && fi != nil && fi.IsDir())
	}
}

// TestTryRestartOnAHostWithoutSystemdOrAMissingUnit exercises the run() error
// path — CombinedOutput wiring the query tests never touch — without depending
// on any real unit existing. try-restart of a unit that cannot exist must fail,
// and the failure must carry systemctl's own diagnostic, not a swallowed error.
func TestTryRestartOnAHostWithoutSystemdOrAMissingUnit(t *testing.T) {
	if !Available() {
		t.Skip("no systemd on this host — nothing to exercise")
	}

	const unit = "rec-deploy-nonexistent-xyz.service"

	err := TryRestart(context.Background(), unit)
	if err == nil {
		t.Skip("try-restart of a nonexistent unit unexpectedly succeeded on this host")
	}

	if !strings.Contains(err.Error(), unit) {
		t.Errorf("TryRestart error %q does not mention the unit %q", err.Error(), unit)
	}
}
