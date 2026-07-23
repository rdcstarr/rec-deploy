package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/systemd"
)

// TestMissingScopesErrorNamesTheScope is the point of validating the token at
// all: an old implementation never checks one, so a token without admin:repo_hook is
// only discovered when `repo add` fails to create the webhook. The wizard must
// name the missing scope, not just refuse.
func TestMissingScopesErrorNamesTheScope(t *testing.T) {
	err := missingScopesError([]string{"admin:repo_hook"})

	msg := err.Error()
	if !strings.Contains(msg, "admin:repo_hook") {
		t.Errorf("error %q does not name the missing scope", msg)
	}
	if !strings.Contains(msg, "the admin:repo_hook scope") {
		t.Errorf("error %q does not read as a single missing scope", msg)
	}
	if !strings.Contains(msg, "https://github.com/settings/tokens") {
		t.Errorf("error %q does not say where to regenerate the token", msg)
	}
}

// TestMissingScopesErrorNamesBoth covers a token with neither scope: both are
// named, in one message, so the operator regenerates once.
func TestMissingScopesErrorNamesBoth(t *testing.T) {
	msg := missingScopesError([]string{"repo", "admin:repo_hook"}).Error()

	if !strings.Contains(msg, "repo and admin:repo_hook scopes") {
		t.Errorf("error %q does not name both missing scopes", msg)
	}
}

// TestPublicURLFor checks the webhook URL the wizard proposes: the routed
// address of this host, on the port the daemon listens on.
func TestPublicURLFor(t *testing.T) {
	if got := publicURLFor("1.2.3.4", "0.0.0.0:9000"); got != "http://1.2.3.4:9000" {
		t.Errorf("publicURLFor = %q, want http://1.2.3.4:9000", got)
	}
	if got := publicURLFor("1.2.3.4", "127.0.0.1:8080"); got != "http://1.2.3.4:8080" {
		t.Errorf("publicURLFor = %q, want http://1.2.3.4:8080", got)
	}
}

// TestPublicURLForIPv6 checks that an IPv6 address is bracketed, or the URL is
// unusable.
func TestPublicURLForIPv6(t *testing.T) {
	if got := publicURLFor("2001:db8::1", "[::]:9000"); got != "http://[2001:db8::1]:9000" {
		t.Errorf("publicURLFor = %q, want http://[2001:db8::1]:9000", got)
	}
}

// TestPublicURLForFallbacks covers the two degenerate inputs: no route to the
// internet (no address to propose at all), and a listen address the port cannot
// be read from (the default port is still a better guess than none).
func TestPublicURLForFallbacks(t *testing.T) {
	if got := publicURLFor("", "0.0.0.0:9000"); got != "" {
		t.Errorf("publicURLFor with no address = %q, want empty", got)
	}
	if got := publicURLFor("1.2.3.4", "nonsense"); got != "http://1.2.3.4:9000" {
		t.Errorf("publicURLFor = %q, want the default port", got)
	}
}

// TestInitNonInteractive is the contract for a piped, CI or systemd run: the
// wizard cannot prompt, so it must point at the flag-driven way in instead of
// hanging or failing vaguely.
func TestInitNonInteractive(t *testing.T) {
	cmd := newInitCmd()

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("init ran without a terminal, want an error")
	}
	if !strings.Contains(err.Error(), "rec-deploy config set") {
		t.Errorf("error %q does not point at `rec-deploy config set`", err)
	}
}

// TestInitAutoUpdateIsSkippedWithoutSystemd: the wizard must not ask a question
// whose answer it cannot act on. A Mac or a container has no timer to enable.
func TestInitAutoUpdateIsSkippedWithoutSystemd(t *testing.T) {
	if systemd.Available() {
		t.Skip("this host runs systemd; the skip path cannot be exercised here")
	}

	// It returns nil without ever reaching ui.Confirm, which would block on a
	// terminal that is not there.
	if err := initAutoUpdate(context.Background()); err != nil {
		t.Fatalf("initAutoUpdate: %v", err)
	}
}

// TestInitializedIsNilSafe pins that the hub can ask whether this server is set
// up before any command has loaded a config — cmd.cfg is nil until
// PersistentPreRunE runs.
func TestInitializedIsNilSafe(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()

	cfg = nil
	if initialized() {
		t.Error("a server with no loaded config must not read as initialized")
	}

	cfg = &config.Config{Initialized: true}
	if !initialized() {
		t.Error("a config with the flag set must read as initialized")
	}
}

// TestInitMCPSkipsAnEnabledEndpoint pins the re-run repair: install.sh runs init
// on upgrades too, and enableCloudflareMCP errors on an endpoint that already
// exists. A step error abandons every step after it, which used to take
// notifications, auto-update and the summary with it.
func TestInitMCPSkipsAnEnabledEndpoint(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()

	cfg = &config.Config{MCP: config.MCPConfig{Enabled: true, Mode: "cloudflare", PublicURL: "https://mcp.example.com/mcp"}}
	if err := initMCP(context.Background(), cfg); err != nil {
		t.Fatalf("initMCP must not fail on an already-enabled endpoint: %v", err)
	}
}
