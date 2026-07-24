package buildinfo

import (
	"strings"
	"testing"
)

// TestResolvedAlwaysCarriesTheTagsV pins the one rule every version surface
// depends on. The three build paths disagree — GoReleaser injects the tag with
// its "v" stripped, the Makefile's `git describe` and debug.ReadBuildInfo() keep
// it — and the release tag a binary compares itself against always has one. A
// released binary therefore reported "0.8.0 → v0.9.0" until Resolved normalised
// the running version here, once, for every caller.
func TestResolvedAlwaysCarriesTheTagsV(t *testing.T) {
	saved := Version
	defer func() { Version = saved }()

	tests := []struct {
		name, version, want string
	}{
		{"goreleaser strips the v", "0.8.0", "v0.8.0"},
		{"a tag that kept its v is left alone", "v0.9.0", "v0.9.0"},
		{"git describe's dirty suffix survives", "v0.9.0-dirty", "v0.9.0-dirty"},
		{"a prerelease is still a version", "1.0.0-rc.1", "v1.0.0-rc.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version = tt.version
			if got := Resolved(); got != tt.want {
				t.Errorf("Resolved() with Version=%q = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

// TestResolvedLeavesTheDevFallbackAlone guards the reason withV tests the first
// byte instead of just prefixing: a plain `go build` leaves Version at "dev",
// and "vdev" would be printed under the banner and posted in every deploy
// notification a developer's build sent.
func TestResolvedLeavesTheDevFallbackAlone(t *testing.T) {
	saved := Version
	defer func() { Version = saved }()

	// "dev" is the sentinel Resolved falls back through; in a test binary
	// debug.ReadBuildInfo() reports no usable module version, so it stays.
	Version = "dev"
	if got := Resolved(); strings.HasPrefix(got, "v") {
		t.Errorf("Resolved() = %q, want no v on a non-numeric version", got)
	}
}

// TestStringReportsTheResolvedVersion pins that `--version` reads the normalised
// value rather than the raw ldflags one — the two differ for every released
// binary, and this is the line an operator quotes in a bug report.
func TestStringReportsTheResolvedVersion(t *testing.T) {
	saved := Version
	defer func() { Version = saved }()

	Version = "0.8.0"
	if got := String(); !strings.HasPrefix(got, "v0.8.0 ") {
		t.Errorf("String() = %q, want it to open with the resolved version", got)
	}
}
