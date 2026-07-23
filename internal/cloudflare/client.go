// Package cloudflare provisions the narrowly-scoped Cloudflare Tunnel used by
// the remote MCP endpoint. Provisioning credentials are accepted by callers but
// never persisted here.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const apiURL = "https://api.cloudflare.com/client/v4"

// Client calls the small Cloudflare API surface needed for MCP provisioning.
type Client struct {
	token string
	base  string
	http  *http.Client
}

// NewClient creates a Cloudflare API client using an ephemeral provisioning token.
func NewClient(token string) *Client {
	return &Client{token: token, base: apiURL, http: &http.Client{Timeout: 30 * time.Second}}
}

// Zone is a Cloudflare DNS zone visible to the provisioning token.
type Zone struct{ ID, Name, AccountID string }

// Tunnel is a Cloudflare Tunnel and its tunnel-specific runtime credential.
type Tunnel struct{ ID, Name, AccountID, Secret string }

// DNSRecord identifies an existing DNS record and its zone.
type DNSRecord struct{ ID, ZoneID, ZoneName, Content string }

type envelope[T any] struct {
	Success bool `json:"success"`
	Result  T    `json:"result"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// VerifyAccount confirms that an account-owned token is active for accountID.
func (c *Client) VerifyAccount(ctx context.Context, accountID string) error {
	var out envelope[map[string]any]
	return c.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(accountID)+"/tokens/verify", nil, &out)
}

// Zones lists active zones available to the token.
func (c *Client) Zones(ctx context.Context) ([]Zone, error) {
	var out envelope[[]struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Status  string `json:"status"`
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
	}]
	if err := c.do(ctx, http.MethodGet, "/zones?status=active&per_page=50", nil, &out); err != nil {
		return nil, err
	}
	items := make([]Zone, 0, len(out.Result))
	for _, v := range out.Result {
		items = append(items, Zone{ID: v.ID, Name: v.Name, AccountID: v.Account.ID})
	}
	return items, nil
}

// CreateTunnel creates a locally-managed tunnel with a caller-generated secret.
func (c *Client) CreateTunnel(ctx context.Context, accountID, name string, secret []byte) (Tunnel, error) {
	body := map[string]any{"name": name, "config_src": "local", "tunnel_secret": base64.StdEncoding.EncodeToString(secret)}
	var out envelope[struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		AccountTag string `json:"account_tag"`
	}]
	if err := c.do(ctx, http.MethodPost, "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel", body, &out); err != nil {
		return Tunnel{}, err
	}
	return Tunnel{ID: out.Result.ID, Name: out.Result.Name, AccountID: out.Result.AccountTag, Secret: base64.StdEncoding.EncodeToString(secret)}, nil
}

// CreateDNS creates the proxied hostname pointing at tunnel.
func (c *Client) CreateDNS(ctx context.Context, zoneID, hostname, tunnelID string) (string, error) {
	body := map[string]any{"type": "CNAME", "name": hostname, "content": tunnelID + ".cfargotunnel.com", "proxied": true}
	var out envelope[struct {
		ID string `json:"id"`
	}]
	if err := c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", body, &out); err != nil {
		return "", err
	}
	return out.Result.ID, nil
}

// FindDNS finds hostname in the longest matching active zone visible to the token.
func (c *Client) FindDNS(ctx context.Context, hostname string) (DNSRecord, error) {
	zones, err := c.Zones(ctx)
	if err != nil {
		return DNSRecord{}, err
	}
	sort.Slice(zones, func(i, j int) bool { return len(zones[i].Name) > len(zones[j].Name) })
	for _, zone := range zones {
		if hostname != zone.Name && !strings.HasSuffix(hostname, "."+zone.Name) {
			continue
		}
		var out envelope[[]struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Content string `json:"content"`
		}]
		path := "/zones/" + url.PathEscape(zone.ID) + "/dns_records?type=CNAME&name=" + url.QueryEscape(hostname)
		if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
			return DNSRecord{}, err
		}
		if len(out.Result) == 0 {
			return DNSRecord{ZoneID: zone.ID, ZoneName: zone.Name}, nil
		}
		v := out.Result[0]
		return DNSRecord{ID: v.ID, ZoneID: zone.ID, ZoneName: zone.Name, Content: v.Content}, nil
	}
	return DNSRecord{}, fmt.Errorf("hostname %q is not in an active zone visible to the token", hostname)
}

// DeleteDNS removes the exact DNS record created during provisioning.
func (c *Client) DeleteDNS(ctx context.Context, zoneID, recordID string) error {
	var out envelope[map[string]any]
	return c.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, &out)
}

// DeleteTunnel removes a tunnel created during provisioning.
func (c *Client) DeleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	var out envelope[map[string]any]
	return c.do(ctx, http.MethodDelete, "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel/"+url.PathEscape(tunnelID), nil, &out)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("read cloudflare response: %w", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode cloudflare response (HTTP %d): %w", resp.StatusCode, err)
	}
	var failed envelope[json.RawMessage]
	_ = json.Unmarshal(b, &failed)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !failed.Success {
		messages := make([]string, 0, len(failed.Errors))
		for _, e := range failed.Errors {
			messages = append(messages, e.Message)
		}
		return fmt.Errorf("cloudflare API HTTP %d: %s", resp.StatusCode, strings.Join(messages, "; "))
	}
	return nil
}
