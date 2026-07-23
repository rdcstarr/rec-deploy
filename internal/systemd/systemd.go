// Package systemd is the seam between rec-deploy and the init system: the few
// systemctl calls the self-update flow and `rec-deploy status` need. It is a
// wrapper over os/exec and nothing more — no unit generation, no D-Bus.
package systemd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Available reports whether this host is actually running systemd. systemctl on
// PATH is not enough: it is installed inside containers whose PID 1 is not
// systemd, where every call fails with a confusing message. The directory check
// is what sd_booted(3) itself does.
func Available() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}

	fi, err := os.Stat("/run/systemd/system")

	return err == nil && fi.IsDir()
}

// TryRestart restarts unit only if it is already running, so a host where the
// operator deliberately keeps the daemon stopped stays stopped.
func TryRestart(ctx context.Context, unit string) error {
	return run(ctx, "try-restart", unit)
}

// IsActive reports whether unit is running.
func IsActive(ctx context.Context, unit string) bool {
	return query(ctx, "is-active", unit) == "active"
}

// IsEnabled reports whether unit starts at boot.
//
// It cannot tell a disabled unit from one that is absent or masked — all three
// answer false, because `is-enabled` prints "not-found" for the first and fails
// for the last. Ask LoadState when the difference matters, or the advice you
// print will be impossible to follow.
func IsEnabled(ctx context.Context, unit string) bool {
	return query(ctx, "is-enabled", unit) == "enabled"
}

// Load states worth naming. systemd has more, but these are the ones that change
// what an operator must do next.
const (
	// LoadNotFound means no unit file of that name exists.
	LoadNotFound = "not-found"
	// LoadMasked means the unit is symlinked to /dev/null and cannot be started or
	// enabled until it is unmasked.
	LoadMasked = "masked"
	// LoadLoaded means systemd read the unit successfully.
	LoadLoaded = "loaded"
)

// LoadState returns systemd's own word for how it loaded unit — one of the
// constants above, or something rarer like "error" or "bad-setting".
//
// A bool cannot answer this honestly: "not installed", "masked" and "installed
// but off" each need different advice, and folding them together is how the
// status output came to tell operators to enable a unit that did not exist.
// `show` is the question to ask, not `is-enabled`, which prints "not-found" and
// exits non-zero for an unknown unit — indistinguishable from "disabled" once it
// is squeezed into a bool.
func LoadState(ctx context.Context, unit string) string {
	return query(ctx, "show", "-p", "LoadState", "--value", unit)
}

// FragmentPath returns the unit file systemd actually resolved, or "" if it found
// none.
//
// The resolved path, never a guessed one: /etc/systemd/system shadows
// /lib/systemd/system, so a box that took units from two installers runs the copy
// systemd picked and not the one a directory rule would name.
func FragmentPath(ctx context.Context, unit string) string {
	return query(ctx, "show", "-p", "FragmentPath", "--value", unit)
}

// EnableNow enables unit at boot and starts it immediately.
func EnableNow(ctx context.Context, unit string) error {
	return run(ctx, "enable", "--now", unit)
}

// Restart restarts unit, starting it when it is currently stopped.
func Restart(ctx context.Context, unit string) error { return run(ctx, "restart", unit) }

// DisableNow disables unit at boot and stops it immediately.
func DisableNow(ctx context.Context, unit string) error {
	return run(ctx, "disable", "--now", unit)
}

// Reload makes systemd re-read the unit files after one was added or removed.
func Reload(ctx context.Context) error {
	return run(ctx, "daemon-reload")
}

// run executes a systemctl subcommand, carrying its output into the error —
// systemctl says exactly what went wrong and the caller must not swallow it.
func run(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}

// query runs a systemctl question whose answer is one word on stdout. It
// ignores the exit status on purpose: `is-active` exits non-zero for a unit that
// is merely inactive, which is an answer, not an error.
func query(ctx context.Context, args ...string) string {
	out, _ := exec.CommandContext(ctx, "systemctl", args...).Output()

	return strings.TrimSpace(string(out))
}
