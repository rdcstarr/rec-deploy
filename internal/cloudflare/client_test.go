package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvisioningAPI(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization header was not sent")
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/accounts/a1/tokens/verify":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{}})
		case "/zones":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{map[string]any{"id": "z1", "name": "example.com", "status": "active", "account": map[string]any{"id": "a1"}}}})
		case "/accounts/a1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "t1", "name": "rec-deploy", "account_tag": "a1"}})
		case "/zones/z1/dns_records":
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "d1"}})
			}
		case "/zones/z1/dns_records/d1", "/accounts/a1/cfd_tunnel/t1":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient("secret")
	c.base, c.http = srv.URL, srv.Client()
	ctx := context.Background()
	if err := c.VerifyAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	zones, err := c.Zones(ctx)
	if err != nil || len(zones) != 1 || zones[0].AccountID != "a1" {
		t.Fatalf("Zones = %+v, %v", zones, err)
	}
	if record, err := c.FindDNS(ctx, "mcp.example.com"); err != nil || record.ZoneID != "z1" || record.ID != "" {
		t.Fatalf("FindDNS = %+v, %v", record, err)
	}
	tunnel, err := c.CreateTunnel(ctx, "a1", "rec-deploy", make([]byte, 32))
	if err != nil || tunnel.ID != "t1" {
		t.Fatalf("CreateTunnel = %+v, %v", tunnel, err)
	}
	record, err := c.CreateDNS(ctx, "z1", "mcp.example.com", tunnel.ID)
	if err != nil || record != "d1" {
		t.Fatalf("CreateDNS = %q, %v", record, err)
	}
	if err := c.DeleteDNS(ctx, "z1", record); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteTunnel(ctx, "a1", tunnel.ID); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 8 {
		t.Fatalf("calls = %v", calls)
	}
}

func TestWriteRuntimeKeepsCredentialPrivateAndCatchAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mcp")
	err := WriteRuntime(dir, Tunnel{ID: "id", AccountID: "account", Secret: "secret"}, "mcp.example.com", "127.0.0.1:8765")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tunnel.json", "cloudflared.yml"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o", name, fi.Mode().Perm())
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "cloudflared.yml"))
	s := string(b)
	for _, want := range []string{"service: http://127.0.0.1:8765", "service: http_status:404"} {
		if !strings.Contains(s, want) {
			t.Errorf("config missing %q: %s", want, s)
		}
	}
}

func TestDigestFromReleaseBodyRequiresExactAsset(t *testing.T) {
	body := "### SHA256 Checksums:\n`cloudflared-linux-amd64: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\ncloudflared-linux-arm64: ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff`"
	got := digestFromReleaseBody(body, "cloudflared-linux-amd64")
	if got != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("digest = %q", got)
	}
	if got := digestFromReleaseBody(body, "cloudflared-linux-amd"); got != "" {
		t.Fatalf("partial asset matched: %q", got)
	}
}
