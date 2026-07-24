// Package buildinfo exposes the binary's version metadata, injected at link
// time via -ldflags and falling back to the Go module build info.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Build metadata, overridden at link time by GoReleaser / the Makefile via
// -X github.com/rdcstarr/rec-deploy/internal/buildinfo.Version=... and friends.
//
// Read Version through Resolved, never directly: GoReleaser injects
// {{ .Version }}, which is the tag with its leading "v" stripped, so the raw
// value disagrees with every other source of the same number.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Resolved reports the effective version, preferring the ldflags value and
// falling back to the module version recorded by `go install`.
//
// It always carries the leading "v" of the git tag the binary was built from.
// Only GoReleaser drops it — `git describe` (the Makefile) and
// debug.ReadBuildInfo() both keep it — so a released binary called itself
// "0.8.0" while the release it compared itself against was "v0.9.0", and every
// surface showing both rendered the mismatch: "0.8.0 → v0.9.0" in the update
// notification, "· 0.9.0" under the banner, a `version --json` no script could
// match against a tag.
func Resolved() string {
	version := Version
	if version == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
	}

	return withV(version)
}

// withV restores the leading "v" on a version number that lost it. Only a value
// starting with a digit is a version number, which is what keeps the "dev"
// fallback from rendering as "vdev".
func withV(version string) string {
	if version == "" || version[0] < '0' || version[0] > '9' {
		return version
	}

	return "v" + version
}

// String returns a human-readable one-line build summary.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s %s/%s)",
		Resolved(), Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
