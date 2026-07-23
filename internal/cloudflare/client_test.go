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

// TestSetDNSContentRepointsInPlace covers taking over a hostname an earlier
// install published. The record has to be updated where it stands — a delete
// followed by a create would drop the name out of DNS in between, and would
// leave nothing to put back if the rest of provisioning then failed.
func TestSetDNSContentRepointsInPlace(t *testing.T) {
	var method, path string
	var body map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "d1"}})
	}))
	defer srv.Close()

	c := NewClient("secret")
	c.base, c.http = srv.URL, srv.Client()

	if err := c.SetDNSContent(context.Background(), "z1", "d1", "mcp.example.com", TunnelTarget("t2")); err != nil {
		t.Fatalf("SetDNSContent: %v", err)
	}

	if method != http.MethodPut {
		t.Errorf("method = %s, want PUT so the record is updated rather than replaced", method)
	}
	if path != "/zones/z1/dns_records/d1" {
		t.Errorf("path = %s, want the existing record's own path", path)
	}
	if body["content"] != "t2"+TunnelDomain {
		t.Errorf("content = %v, want the new tunnel's target", body["content"])
	}
	if body["proxied"] != true {
		t.Errorf("proxied = %v, want the record to stay proxied", body["proxied"])
	}
}

// TestTunnelTargetIsWhatCreateDNSWrites keeps the takeover and the create path
// pointing at the same place: a rollback that restored a differently-shaped
// target would silently break the endpoint it was meant to save.
func TestTunnelTargetIsWhatCreateDNSWrites(t *testing.T) {
	var created string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content string `json:"content"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		created = body.Content
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "d1"}})
	}))
	defer srv.Close()

	c := NewClient("secret")
	c.base, c.http = srv.URL, srv.Client()

	if _, err := c.CreateDNS(context.Background(), "z1", "mcp.example.com", "t1"); err != nil {
		t.Fatalf("CreateDNS: %v", err)
	}
	if created != TunnelTarget("t1") {
		t.Errorf("CreateDNS wrote %q but TunnelTarget builds %q", created, TunnelTarget("t1"))
	}
}
