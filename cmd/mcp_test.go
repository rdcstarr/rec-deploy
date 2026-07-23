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

func TestEnabledMCPMenuExposesConnectionWithoutTokenSubmenu(t *testing.T) {
	options := mcpMenuOptions(&config.Config{MCP: config.MCPConfig{Enabled: true, Listen: "127.0.0.1:8765"}})
	seen := make(map[string]bool, len(options))
	for _, option := range options {
		seen[option.Value] = true
	}
	for _, want := range []string{"client-config", "show-token", "cloudflare", "disable"} {
		if !seen[want] {
			t.Errorf("enabled MCP menu does not include %q: %v", want, seen)
		}
	}
	if seen["token"] {
		t.Errorf("enabled MCP menu still requires the nested token menu: %v", seen)
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
