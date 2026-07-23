package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Listen != "0.0.0.0:9000" {
		t.Errorf("Listen = %q, want 0.0.0.0:9000", cfg.Listen)
	}
	if len(cfg.Discovery.Prune) != 4 {
		t.Errorf("Prune = %v, want 4 defaults", cfg.Discovery.Prune)
	}
	if len(cfg.Discovery.Roots) != 2 {
		t.Errorf("Roots = %v, want 2 defaults", cfg.Discovery.Roots)
	}
	if cfg.MCP.Listen != "0.0.0.0:8765" || cfg.MCP.Enabled {
		t.Errorf("MCP = %+v, want disabled on 0.0.0.0:8765", cfg.MCP)
	}
}

func TestLoadMalformedIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("listen: [unterminated\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Error("Load of a malformed config returned no error — it must never fall back to defaults silently")
	}
}

func TestSaveRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	cfg := &Config{
		Listen:    "127.0.0.1:9000",
		PublicURL: "http://1.2.3.4:9000",
		GitHub:    GitHubConfig{Token: "ghp_secret"},
		Discovery: DiscoveryConfig{Roots: []string{"/var/www"}, Prune: []string{"vendor"}},
		Notify:    NotifyConfig{Telegram: TelegramConfig{Token: "t", ChatID: "42"}},
		MCP:       MCPConfig{Enabled: true, Listen: "127.0.0.1:8765", TokenHash: "digest", Mode: "cloudflare", PublicURL: "https://mcp.example.com/mcp", Cloudflare: CloudflareConfig{AccountID: "a", APIToken: "cf_secret", ZoneID: "z", Hostname: "mcp.example.com", TunnelID: "t", DNSRecordID: "d"}},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600 — the file holds a GitHub token", perm)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.GitHub.Token != "ghp_secret" || got.PublicURL != cfg.PublicURL {
		t.Errorf("round trip lost data: %+v", got)
	}
	if len(got.Discovery.Roots) != 1 || got.Discovery.Roots[0] != "/var/www" {
		t.Errorf("Roots = %v, want [/var/www]", got.Discovery.Roots)
	}
	if got.Notify.Telegram.ChatID != "42" {
		t.Errorf("ChatID = %q, want 42", got.Notify.Telegram.ChatID)
	}
	if !got.MCP.Enabled || got.MCP.Listen != "127.0.0.1:8765" || got.MCP.TokenHash != "digest" {
		t.Errorf("MCP round trip = %+v", got.MCP)
	}
	if got.MCP.Mode != "cloudflare" || got.MCP.Cloudflare.TunnelID != "t" || got.MCP.PublicURL != "https://mcp.example.com/mcp" {
		t.Errorf("Cloudflare MCP round trip = %+v", got.MCP)
	}
	if got.MCP.Cloudflare.APIToken != "cf_secret" {
		t.Error("Cloudflare API token was not preserved")
	}
}

// TestSavePreservesUnmanagedKeys guards the Save contract: a key rec-deploy does not
// manage (hand-added, or written by a newer version) must survive a rewrite.
func TestSavePreservesUnmanagedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("custom_key: keep-me\nlisten: 0.0.0.0:1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Listen = "0.0.0.0:2"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "custom_key") {
		t.Errorf("Save dropped the unmanaged key: %s", data)
	}
}

func TestConfiguredPredicates(t *testing.T) {
	if (TelegramConfig{Token: "t"}).Configured() {
		t.Error("telegram with no chat ID must not be Configured")
	}
	if !(TelegramConfig{Token: "t", ChatID: "1"}).Configured() {
		t.Error("telegram with token+chat ID must be Configured")
	}
	if (EmailConfig{SMTP: "smtp:587", From: "a@b"}).Configured() {
		t.Error("email with no recipient must not be Configured")
	}
	if !(EmailConfig{SMTP: "smtp:587", From: "a@b", To: "c@d"}).Configured() {
		t.Error("email with smtp+from+to must be Configured")
	}
}
