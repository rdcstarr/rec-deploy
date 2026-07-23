package cmd

import (
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/cloudflare"
	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

func TestMCPCommandIsVisibleInInteractiveHub(t *testing.T) {
	cmd := newMCPCmd()
	if cmd.Annotations[annotationInteractive] == "false" {
		t.Error("MCP command is excluded from the interactive hub")
	}
	if cmd.Example == "" {
		t.Error("MCP help does not explain how a client launches it")
	}
}

func TestAvailableMCPListenSkipsOccupiedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	got, err := availableMCPlisten(port, port+1)
	if err != nil {
		t.Fatal(err)
	}
	if got == ln.Addr().String() {
		t.Fatalf("selected occupied address %s", got)
	}
}

func TestCloudflareAccountIDAndZoneFiltering(t *testing.T) {
	const account = "0123456789abcdef0123456789abcdef"
	if !cloudflareAccountID.MatchString(account) || cloudflareAccountID.MatchString("not-an-account") {
		t.Fatal("Cloudflare Account ID validation is incorrect")
	}
	zones := zonesForAccount([]cloudflare.Zone{
		{Name: "wanted.example", AccountID: account},
		{Name: "other.example", AccountID: "ffffffffffffffffffffffffffffffff"},
	}, strings.ToUpper(account))
	if len(zones) != 1 || zones[0].Name != "wanted.example" {
		t.Fatalf("zonesForAccount = %+v", zones)
	}
}

func TestCloudflareEndpointWins(t *testing.T) {
	cfg := &config.Config{PublicURL: "http://203.0.113.1:9000", MCP: config.MCPConfig{PublicURL: "https://mcp.example.com/mcp", Listen: "127.0.0.1:8765"}}
	if got := mcpEndpoint(cfg); got != "https://mcp.example.com/mcp" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestMCPMenuDescriptionsShareOneColumn(t *testing.T) {
	ui.SetColor(false)
	options := mcpMenuOptions(&config.Config{MCP: config.MCPConfig{Listen: "0.0.0.0:8765"}})
	columns := make(map[int]string)
	states := []string{"off · 0.0.0.0:8765", "guided Cloudflare HTTPS setup", "credentials not configured", "not created"}
	for i, option := range options {
		index := strings.Index(option.Label, states[i])
		columns[index] = option.Value
	}
	if len(columns) != 1 {
		t.Errorf("MCP descriptions start in different columns: %v", columns)
	}
}

func TestMCPMenuOffersOnlyValidLifecycleAction(t *testing.T) {
	for _, test := range []struct {
		name     string
		enabled  bool
		want     string
		unwanted string
	}{
		{name: "disabled", want: "enable", unwanted: "disable"},
		{name: "enabled", enabled: true, want: "disable", unwanted: "enable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := mcpMenuOptions(&config.Config{MCP: config.MCPConfig{Enabled: test.enabled}})
			seen := make(map[string]bool, len(options))
			for _, option := range options {
				seen[option.Value] = true
			}
			if !seen[test.want] || seen[test.unwanted] {
				t.Errorf("menu actions = %v, want %q without %q", seen, test.want, test.unwanted)
			}
		})
	}
}

// TestEnabledMCPMenuCanRotateTheToken pins a reachability bug closed: with MCP
// enabled the menu used to offer show-token, which only views — so rotation,
// needed on exactly the servers that have a token, was unreachable from any
// menu. The entry now opens the token menu, which both reveals and rotates.
func TestEnabledMCPMenuCanRotateTheToken(t *testing.T) {
	options := mcpMenuOptions(&config.Config{MCP: config.MCPConfig{Enabled: true, Listen: "127.0.0.1:8765"}})
	seen := make(map[string]bool, len(options))
	for _, option := range options {
		seen[option.Value] = true
	}

	for _, want := range []string{"status", "client-config", "token", "cloudflare", "disable"} {
		if !seen[want] {
			t.Errorf("enabled MCP menu does not include %q: %v", want, seen)
		}
	}
	if seen["show-token"] {
		t.Errorf("enabled MCP menu still points at the view-only token command: %v", seen)
	}
}

// TestMCPStatusRowsDropRedundantFacts pins the de-noised status: remote, mode
// and public HTTPS were the same bit — they are set and cleared together — and
// listen is already inside endpoint. token was always "set" when MCP is on,
// because serve refuses to start otherwise.
func TestMCPStatusRowsDropRedundantFacts(t *testing.T) {
	off := mcpStatusRows(&config.Config{MCP: config.MCPConfig{Listen: "0.0.0.0:8765"}})
	if len(off) != 1 || off[0][0] != "access" || off[0][1] != "off" {
		t.Fatalf("a disabled endpoint needs one row, got %+v", off)
	}

	on := mcpStatusRows(&config.Config{MCP: config.MCPConfig{Enabled: true, Listen: "0.0.0.0:8765"}})
	keys := make([]string, 0, len(on))
	for _, row := range on {
		keys = append(keys, row[0])
	}
	for _, unwanted := range []string{"remote", "mode", "listen", "token", "public HTTPS", "MCP service", "Cloudflare tunnel"} {
		for _, key := range keys {
			if key == unwanted {
				t.Errorf("status still shows the redundant row %q: %v", unwanted, keys)
			}
		}
	}
	if len(on) < 3 {
		t.Errorf("status lost a fact it is the only place to read: %v", keys)
	}
}

// TestMCPFoldServiceStatesSplitsOnlyOnDisagreement pins the fold/split
// decision mcpServiceState delegates to, independent of systemd: a healthy
// server spends one row saying so, and a disagreement is spelled out rather
// than hidden behind whichever state happened to be read first.
func TestMCPFoldServiceStatesSplitsOnlyOnDisagreement(t *testing.T) {
	for _, test := range []struct {
		name        string
		mcp, tunnel string
		want        string
	}{
		{name: "both active", mcp: "active", tunnel: "active", want: "active"},
		{name: "both inactive", mcp: "inactive", tunnel: "inactive", want: "inactive"},
		{name: "mcp active tunnel inactive", mcp: "active", tunnel: "inactive", want: "active · tunnel inactive"},
		{name: "mcp inactive tunnel active", mcp: "inactive", tunnel: "active", want: "inactive · tunnel active"},
		{name: "systemd unavailable on both", mcp: "unavailable", tunnel: "unavailable", want: "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := foldServiceStates(test.mcp, test.tunnel); got != test.want {
				t.Errorf("foldServiceStates(%q, %q) = %q, want %q", test.mcp, test.tunnel, got, test.want)
			}
		})
	}
}

// TestMCPAccessStateNamesTheTransport pins the one row that replaces three.
func TestMCPAccessStateNamesTheTransport(t *testing.T) {
	for _, test := range []struct {
		name string
		cfg  config.MCPConfig
		want string
	}{
		{name: "off", cfg: config.MCPConfig{}, want: "off"},
		{name: "local", cfg: config.MCPConfig{Enabled: true}, want: "local HTTP"},
		{name: "cloudflare", cfg: config.MCPConfig{Enabled: true, Mode: "cloudflare"}, want: "Cloudflare tunnel"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mcpAccessState(&config.Config{MCP: test.cfg}); got != test.want {
				t.Errorf("mcpAccessState = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCloudflareCredentialSummaryNeverExposesToken(t *testing.T) {
	const secret = "cloudflare-secret-token"
	summary := cloudflareCredentialSummary(config.CloudflareConfig{
		AccountID: "0123456789abcdef0123456789abcdef",
		APIToken:  secret,
	})
	if strings.Contains(summary, secret) || summary != "Account ID and API token configured" {
		t.Fatalf("unsafe Cloudflare summary %q", summary)
	}
}

func TestMCPClientJSONContainsReadyConnection(t *testing.T) {
	body, err := mcpClientJSON("https://mcp.example.com/mcp", "rdmcp_secret")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("client config is not JSON: %v", err)
	}
	server := got["mcpServers"].(map[string]any)["rec-deploy"].(map[string]any)
	if server["url"] != "https://mcp.example.com/mcp" || server["type"] != "http" {
		t.Fatalf("server config = %#v", server)
	}
	headers := server["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer rdmcp_secret" {
		t.Fatalf("Authorization = %#v", headers["Authorization"])
	}
}

func TestMCPEndpointUsesConfiguredPublicHost(t *testing.T) {
	for _, test := range []struct {
		name string
		cfg  config.Config
		want string
	}{
		{
			name: "public IPv4",
			cfg:  config.Config{PublicURL: "http://203.0.113.10:9000", MCP: config.MCPConfig{Listen: "0.0.0.0:8765"}},
			want: "http://203.0.113.10:8765/mcp",
		},
		{
			name: "public hostname",
			cfg:  config.Config{PublicURL: "https://deploy.example.com", MCP: config.MCPConfig{Listen: "0.0.0.0:8765"}},
			want: "http://deploy.example.com:8765/mcp",
		},
		{
			name: "explicit MCP host",
			cfg:  config.Config{MCP: config.MCPConfig{Listen: "192.0.2.5:8765"}},
			want: "http://192.0.2.5:8765/mcp",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mcpEndpoint(&test.cfg); got != test.want {
				t.Errorf("mcpEndpoint = %q, want %q", got, test.want)
			}
		})
	}
}

// TestRandomMCPSubdomainDiffersPerCall is why the default changed. It used to be
// derived from the server's hostname, so it was identical on every install of
// the same box — reinstalling landed on the endpoint the previous install had
// published and the collision had to be resolved mid-setup.
func TestRandomMCPSubdomainDiffersPerCall(t *testing.T) {
	seen := make(map[string]bool, 16)
	for i := 0; i < 16; i++ {
		name := randomMCPSubdomain()
		if !strings.HasPrefix(name, "mcp-") {
			t.Fatalf("subdomain %q is not recognisable as an MCP endpoint", name)
		}
		if strings.Contains(name, ".") {
			t.Fatalf("subdomain %q is not a single DNS label", name)
		}
		seen[name] = true
	}
	if len(seen) != 16 {
		t.Errorf("only %d of 16 proposed subdomains were distinct", len(seen))
	}
}

// TestDescribeExistingRecordTellsOurTunnelFromTheirs is what makes "replace it?"
// answerable. A record an earlier install left points at a Cloudflare tunnel and
// is safe to take over; anything else is the operator's own, and replacing it
// breaks whatever uses that name — so the two must not read alike.
func TestDescribeExistingRecordTellsOurTunnelFromTheirs(t *testing.T) {
	ours := describeExistingRecord("mcp-abc.example.com", "a4f1b2c3"+cloudflare.TunnelDomain)
	if !strings.Contains(ours, "earlier rec-deploy install") || !strings.Contains(ours, "same URL") {
		t.Errorf("a tunnel record does not read as safe to take over: %q", ours)
	}
	if strings.Contains(ours, "will break") {
		t.Errorf("a tunnel record was described as dangerous to replace: %q", ours)
	}

	theirs := describeExistingRecord("www.example.com", "some-host.example.net")
	if !strings.Contains(theirs, "rec-deploy did not create") || !strings.Contains(theirs, "will break") {
		t.Errorf("a foreign record was not flagged as dangerous to replace: %q", theirs)
	}
	if !strings.Contains(theirs, "some-host.example.net") {
		t.Errorf("a foreign record does not say what it currently points at: %q", theirs)
	}
}
