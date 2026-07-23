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
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Resolved reports the effective version, preferring the ldflags value and
// falling back to the module version recorded by `go install`.
func Resolved() string {
	if Version != "dev" {
		return Version
	}

	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	return Version
}

// String returns a human-readable one-line build summary.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s %s/%s)",
		Resolved(), Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
