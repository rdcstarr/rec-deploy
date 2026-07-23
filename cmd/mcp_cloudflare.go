package cmd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/cloudflare"
	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/mcpserver"
	"github.com/rdcstarr/rec-deploy/internal/systemd"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

const (
	mcpService       = "rec-deploy-mcp.service"
	mcpTunnelService = "rec-deploy-mcp-tunnel.service"
	mcpUpdateTimer   = "rec-deploy-mcp-update.timer"
)

var hostnamePart = regexp.MustCompile(`[^a-z0-9-]+`)
var cloudflareAccountID = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)

// enableCloudflareMCP provisions the tunnel, the DNS record and the local
// services, rolling every one of them back if a later step fails. report asks
// for the full readiness screen at the end: `mcp enable` wants it, the setup
// wizard does not — there the step is one question among seven, and its answer
// is one line.
func enableCloudflareMCP(ctx context.Context, report bool) error {
	if Config().MCP.Enabled && Config().MCP.Mode == "cloudflare" {
		return fmt.Errorf("cloudflare MCP is already enabled at %s — disable it before provisioning a replacement", Config().MCP.PublicURL)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("cloudflare MCP setup installs system services — run `sudo rec-deploy mcp enable`")
	}
	if !systemd.Available() {
		return fmt.Errorf("cloudflare MCP requires a Linux host running systemd")
	}
	for _, unit := range []string{mcpService, mcpTunnelService, mcpUpdateTimer} {
		if systemd.LoadState(ctx, unit) == systemd.LoadNotFound {
			return fmt.Errorf("%s is not installed — reinstall this rec-deploy release so its verified units are present", unit)
		}
	}

	method, err := ui.Select(ui.ScreenPath("rec-deploy", "MCP", "Cloudflare authorization"), []ui.Option{
		{Label: "API token      recommended · fastest and easiest to clean up", Value: "token"},
		{Label: "Browser login  no API token · opens Cloudflare authorization", Value: "browser"},
	})
	if err != nil || method == "" {
		return err
	}

	dir, err := config.MCPDir()
	if err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, "provision-cert.pem"))
	bin, err := config.CloudflaredBinary()
	if err != nil {
		return err
	}
	if err := ui.Spinner("Installing verified cloudflared…", func() error { _, e := cloudflare.InstallLatest(ctx, bin); return e }); err != nil {
		return err
	}

	listen, err := availableMCPlisten(8765, 8865)
	if err != nil {
		return err
	}
	tunnelName := defaultTunnelName()
	var tunnel cloudflare.Tunnel
	var zone cloudflare.Zone
	var recordID, hostname string
	var provision *cloudflare.Client
	rollback := true
	defer func() {
		if !rollback {
			return
		}
		if provision != nil && recordID != "" {
			if err := provision.DeleteDNS(context.Background(), zone.ID, recordID); err != nil {
				ui.Warn(fmt.Sprintf("rollback could not delete Cloudflare DNS record %s: %v", recordID, err))
			}
		}
		if provision != nil && tunnel.ID != "" {
			if err := provision.DeleteTunnel(context.Background(), zone.AccountID, tunnel.ID); err != nil {
				ui.Warn(fmt.Sprintf("rollback could not delete Cloudflare tunnel %s: %v", tunnel.ID, err))
			}
		}
		if provision == nil && tunnel.ID != "" {
			cleanup := exec.Command(bin, "tunnel", "--origincert", filepath.Join(dir, "provision-cert.pem"), "delete", "-f", tunnel.ID)
			if err := cleanup.Run(); err != nil {
				ui.Warn(fmt.Sprintf("rollback could not delete Cloudflare tunnel %s: %v", tunnel.ID, err))
			}
		}
		_ = os.Remove(filepath.Join(dir, "tunnel.json"))
		_ = os.Remove(filepath.Join(dir, "cloudflared.yml"))
		_ = os.Remove(filepath.Join(dir, "provision-cert.pem"))
	}()

	if method == "token" {
		provision, zone, tunnel, recordID, hostname, err = provisionWithToken(ctx, tunnelName, listen)
	} else {
		zone, tunnel, hostname, err = provisionWithBrowser(ctx, bin, tunnelName, listen)
	}
	if err != nil {
		return err
	}
	if err := cloudflare.WriteRuntime(dir, tunnel, hostname, listen); err != nil {
		return err
	}

	cfg := Config()
	old := cfg.MCP
	var clearToken string
	rotate := cfg.MCP.TokenHash == ""
	if !rotate {
		choice, selectErr := ui.Select("MCP bearer token", []ui.Option{{Label: "Rotate token   recommended", Value: "rotate"}, {Label: "Keep current token", Value: "keep"}})
		if selectErr != nil || choice == "" {
			return selectErr
		}
		rotate = choice == "rotate"
	}
	if rotate {
		clearToken, cfg.MCP.TokenHash, err = mcpserver.NewToken()
		if err != nil {
			return err
		}
	}
	cfg.MCP.Enabled = true
	cfg.MCP.Mode = "cloudflare"
	cfg.MCP.Listen = listen
	cfg.MCP.PublicURL = "https://" + hostname + "/mcp"
	cfg.MCP.Cloudflare = config.CloudflareConfig{
		AccountID:   zone.AccountID,
		APIToken:    cfg.MCP.Cloudflare.APIToken,
		ZoneID:      zone.ID,
		ZoneName:    zone.Name,
		Hostname:    hostname,
		TunnelID:    tunnel.ID,
		TunnelName:  tunnel.Name,
		DNSRecordID: recordID,
	}
	// Quiet: this write is a checkpoint mid-provisioning, not the outcome. The
	// outcome is the readiness report at the end of this function.
	if err := saveQuiet(); err != nil {
		cfg.MCP = old
		return err
	}
	if clearToken != "" {
		if _, err := writeMCPToken(clearToken); err != nil {
			cfg.MCP = old
			_ = save()
			return err
		}
	}
	rollbackLocal := func() {
		_ = systemd.DisableNow(context.Background(), mcpTunnelService)
		_ = systemd.DisableNow(context.Background(), mcpService)
		_ = systemd.DisableNow(context.Background(), mcpUpdateTimer)
		cfg.MCP = old
		_ = save()
		if clearToken != "" {
			if p, e := mcpTokenPath(); e == nil {
				_ = os.Remove(p)
			}
		}
		_ = os.Remove(filepath.Join(dir, "tunnel.json"))
		_ = os.Remove(filepath.Join(dir, "cloudflared.yml"))
	}

	if err := systemd.Reload(ctx); err != nil {
		rollbackLocal()
		return err
	}
	if err := systemd.EnableNow(ctx, mcpService); err != nil {
		rollbackLocal()
		return err
	}
	checkToken := clearToken
	if checkToken == "" {
		checkToken, _ = readMCPToken(cfg.MCP.TokenHash)
	}
	if err := waitMCPOrigin(ctx, listen, checkToken); err != nil {
		rollbackLocal()
		return fmt.Errorf("MCP origin failed to start: %w — inspect `journalctl -u %s`", err, mcpService)
	}
	if err := systemd.EnableNow(ctx, mcpTunnelService); err != nil {
		rollbackLocal()
		return err
	}
	if err := systemd.EnableNow(ctx, mcpUpdateTimer); err != nil {
		rollbackLocal()
		return err
	}
	if err := waitMCPPublic(ctx, cfg.MCP.PublicURL, checkToken); err != nil {
		rollbackLocal()
		return fmt.Errorf("cloudflare tunnel started but public verification failed: %w", err)
	}

	rollback = false
	_ = os.Remove(filepath.Join(dir, "provision-cert.pem"))

	// `mcp enable` was asked for MCP, so its readiness report — bearer token
	// included — is the answer. A wizard step was not: it asked whether to turn
	// the feature on, and answering that with a full screen the operator has to
	// dismiss before the setup continues is an interruption, not a result. The
	// token is not lost with the report; `rec-deploy mcp token` shows it.
	if !report {
		ui.Success("remote MCP online at " + cfg.MCP.PublicURL + " — bearer token with `rec-deploy mcp token`")

		return nil
	}

	displayToken := clearToken
	if displayToken == "" {
		displayToken, _ = readMCPToken(cfg.MCP.TokenHash)
	}
	ui.Success("MCP is online and passed its public connection test")
	err = (ui.Report{
		Title: ui.ScreenPath("rec-deploy", "MCP", "Ready"),
		Rows: [][2]string{
			{"status", "ready"},
			{"public URL", cfg.MCP.PublicURL},
			{"bearer token", displayToken},
			{"client setup", "MCP → Client JSON"},
			{"local service", listen + " (loopback only)"},
		},
	}).Run()
	if errors.Is(err, ui.ErrBack) {
		return nil
	}
	return err
}

func waitMCPOrigin(ctx context.Context, listen, token string) error {
	endpoint := "http://" + listen + "/mcp"
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if systemd.IsActive(ctx, mcpService) {
			err := verifyMCPPublic(ctx, endpoint, token)
			if err == nil {
				return nil
			}
			lastErr = err
		} else {
			lastErr = errors.New("systemd service is not active")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("local endpoint did not become reachable")
	}
	return lastErr
}

func disableCloudflareMCP(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("cloudflare cleanup changes system services — run `sudo rec-deploy mcp disable`")
	}
	cfg := Config()
	if !isInteractive() && !flagYes {
		return fmt.Errorf("cloudflare MCP disable requires confirmation — re-run with `--yes` for local-only shutdown")
	}
	if !isInteractive() && flagYes {
		_ = systemd.DisableNow(ctx, mcpTunnelService)
		_ = systemd.DisableNow(ctx, mcpService)
		_ = systemd.DisableNow(ctx, mcpUpdateTimer)
		cfg.MCP.Enabled = false
		if err := save(); err != nil {
			return err
		}
		ui.Warn("local MCP stopped; Cloudflare resources remain — run interactively to authenticate and delete them")
		return nil
	}
	ok, err := ui.Confirm("Disable Cloudflare MCP and delete its public hostname?", "The tunnel and DNS record created by rec-deploy will be removed. Hestia and other web configuration remain untouched.")
	if err != nil || !ok {
		return err
	}
	accountID := cfg.MCP.Cloudflare.AccountID
	if accountID == "" {
		accountID, err = promptCloudflareAccountID()
		if err != nil {
			return err
		}
	}
	token, err := ui.SecretPrompt("Cloudflare Account API token", "Used to remove the tunnel and DNS record. Alt+R reveals or masks the stored value.", cfg.MCP.Cloudflare.APIToken)
	if err != nil {
		return err
	}
	c := cloudflare.NewClient(strings.TrimSpace(token))
	if err := c.VerifyAccount(ctx, accountID); err != nil {
		return err
	}
	wasActive := systemd.IsActive(ctx, mcpTunnelService)
	if wasActive {
		_ = systemd.DisableNow(ctx, mcpTunnelService)
	}
	cf := cfg.MCP.Cloudflare
	if cf.DNSRecordID == "" && cf.Hostname != "" {
		record, findErr := c.FindDNS(ctx, cf.Hostname)
		if findErr != nil {
			if wasActive {
				_ = systemd.EnableNow(ctx, mcpTunnelService)
			}
			return findErr
		}
		want := cf.TunnelID + ".cfargotunnel.com"
		if record.ID != "" && strings.EqualFold(strings.TrimSuffix(record.Content, "."), want) {
			cf.DNSRecordID, cf.ZoneID, cf.ZoneName = record.ID, record.ZoneID, record.ZoneName
		}
	}
	if cf.DNSRecordID != "" {
		if err := c.DeleteDNS(ctx, cf.ZoneID, cf.DNSRecordID); err != nil {
			if wasActive {
				_ = systemd.EnableNow(ctx, mcpTunnelService)
			}
			return fmt.Errorf("delete Cloudflare DNS record: %w", err)
		}
	}
	if cf.TunnelID != "" {
		if err := c.DeleteTunnel(ctx, cf.AccountID, cf.TunnelID); err != nil {
			return fmt.Errorf("delete Cloudflare tunnel: %w", err)
		}
	}
	_ = systemd.DisableNow(ctx, mcpService)
	_ = systemd.DisableNow(ctx, mcpUpdateTimer)
	dir, _ := config.MCPDir()
	for _, name := range []string{"tunnel.json", "cloudflared.yml", "provision-cert.pem"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	cfg.MCP.Enabled = false
	cfg.MCP.Mode = ""
	cfg.MCP.PublicURL = ""
	cfg.MCP.Cloudflare = config.CloudflareConfig{
		AccountID: cf.AccountID,
		APIToken:  strings.TrimSpace(token),
	}
	if err := save(); err != nil {
		return err
	}
	ui.Success("Cloudflare MCP disabled and its DNS record and tunnel deleted")
	return nil
}

func provisionWithToken(ctx context.Context, tunnelName, listen string) (*cloudflare.Client, cloudflare.Zone, cloudflare.Tunnel, string, string, error) {
	accountID, err := promptCloudflareAccountID()
	if err != nil {
		return nil, cloudflare.Zone{}, cloudflare.Tunnel{}, "", "", err
	}
	// Where to create the token, and with which permissions, belongs in the
	// prompt that asks for it: the description is erased with the form, while a
	// printed line would still be on screen three questions later.
	token, err := ui.SecretPrompt("Cloudflare Account API token",
		"Create it at Manage Account → Account API Tokens → Create Token.\n"+
			"Entire account: Cloudflare One Connector: cloudflared → Edit. Specific domain: DNS → Edit and Zone → Read.\n"+
			"Stored root-only in config.yaml for cleanup and future tunnel changes. Alt+R reveals or masks it.",
		Config().MCP.Cloudflare.APIToken)
	if err != nil {
		return nil, cloudflare.Zone{}, cloudflare.Tunnel{}, "", "", err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, cloudflare.Zone{}, cloudflare.Tunnel{}, "", "", fmt.Errorf("cloudflare API token is required")
	}
	c := cloudflare.NewClient(token)
	if err := ui.Spinner("Validating Cloudflare account access…", func() error { return c.VerifyAccount(ctx, accountID) }); err != nil {
		return nil, cloudflare.Zone{}, cloudflare.Tunnel{}, "", "", err
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		return nil, cloudflare.Zone{}, cloudflare.Tunnel{}, "", "", err
	}
	zones = zonesForAccount(zones, accountID)
	if len(zones) == 0 {
		return nil, cloudflare.Zone{}, cloudflare.Tunnel{}, "", "", fmt.Errorf("the Cloudflare token can access no active DNS zones")
	}
	Config().MCP.Cloudflare.AccountID = accountID
	Config().MCP.Cloudflare.APIToken = token
	if err := config.Save(flagConfig, Config()); err != nil {
		return nil, cloudflare.Zone{}, cloudflare.Tunnel{}, "", "", err
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
	options := make([]ui.Option, 0, len(zones))
	byID := map[string]cloudflare.Zone{}
	for _, z := range zones {
		options = append(options, ui.Option{Label: z.Name, Value: z.ID})
		byID[z.ID] = z
	}
	selected, err := ui.Select("Select Cloudflare zone", options)
	if err != nil || selected == "" {
		return nil, cloudflare.Zone{}, cloudflare.Tunnel{}, "", "", err
	}
	zone := byID[selected]
	hostname, err := promptMCPHostname(zone.Name)
	if err != nil {
		return nil, zone, cloudflare.Tunnel{}, "", "", err
	}
	existing, err := c.FindDNS(ctx, hostname)
	if err != nil {
		return c, zone, cloudflare.Tunnel{}, "", hostname, err
	}
	if existing.ID != "" {
		return c, zone, cloudflare.Tunnel{}, "", hostname, fmt.Errorf("cloudflare DNS record %s already exists — choose another subdomain", hostname)
	}
	ok, err := confirmMCPSetup(hostname, listen, "stored root-only API token")
	if err != nil {
		return nil, zone, cloudflare.Tunnel{}, "", hostname, err
	}
	if !ok {
		return nil, zone, cloudflare.Tunnel{}, "", hostname, ui.ErrBack
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, zone, cloudflare.Tunnel{}, "", "", err
	}
	var tunnel cloudflare.Tunnel
	if err := ui.Spinner("Creating Cloudflare tunnel…", func() error {
		var e error
		tunnel, e = c.CreateTunnel(ctx, zone.AccountID, tunnelName, secret)
		return e
	}); err != nil {
		return c, zone, tunnel, "", hostname, err
	}
	var recordID string
	if err := ui.Spinner("Publishing HTTPS hostname…", func() error { var e error; recordID, e = c.CreateDNS(ctx, zone.ID, hostname, tunnel.ID); return e }); err != nil {
		_ = c.DeleteTunnel(ctx, zone.AccountID, tunnel.ID)
		return c, zone, cloudflare.Tunnel{}, "", hostname, err
	}
	return c, zone, tunnel, recordID, hostname, nil
}

func provisionWithBrowser(ctx context.Context, bin, tunnelName, listen string) (cloudflare.Zone, cloudflare.Tunnel, string, error) {
	home, err := os.MkdirTemp("", "rec-deploy-cloudflare-login-*")
	if err != nil {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, "", err
	}
	defer func() { _ = os.RemoveAll(home) }()
	cmd := exec.CommandContext(ctx, bin, "tunnel", "login")
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	ui.Info("Cloudflare will print an authorization URL. Open it, choose the zone, then return here; setup continues automatically.")
	ui.Info("waiting for Cloudflare browser authorization…")
	if err := cmd.Run(); err != nil {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, "", fmt.Errorf("cloudflare browser login: %w", err)
	}
	cert, err := os.ReadFile(filepath.Join(home, ".cloudflared", "cert.pem"))
	if err != nil {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, "", fmt.Errorf("read Cloudflare account certificate: %w", err)
	}
	mcpDir, err := config.MCPDir()
	if err != nil {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, "", err
	}
	if err := os.WriteFile(filepath.Join(mcpDir, "provision-cert.pem"), cert, 0o600); err != nil {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, "", err
	}
	hostname, err := ui.Prompt("MCP hostname", "Enter a hostname in the Cloudflare zone selected in the browser, for example mcp.example.com.", "")
	if err != nil {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, "", err
	}
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	ok, err := confirmMCPSetup(hostname, listen, "browser authorization")
	if err != nil {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, hostname, err
	}
	if !ok {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, hostname, ui.ErrBack
	}
	cmd = exec.CommandContext(ctx, bin, "tunnel", "create", "--output", "json", tunnelName)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, "", fmt.Errorf("create Cloudflare tunnel: %w", err)
	}
	var created struct{ ID, Name string }
	if err := json.Unmarshal(out, &created); err != nil || created.ID == "" {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, "", fmt.Errorf("decode cloudflared tunnel create output")
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		cleanup := exec.CommandContext(context.Background(), bin, "tunnel", "delete", "-f", created.ID)
		cleanup.Env = append(os.Environ(), "HOME="+home)
		_ = cleanup.Run()
	}()
	cred := filepath.Join(home, ".cloudflared", created.ID+".json")
	b, err := os.ReadFile(cred)
	if err != nil {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, hostname, err
	}
	var v struct{ AccountTag, TunnelSecret, TunnelID string }
	if err := json.Unmarshal(b, &v); err != nil {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, hostname, err
	}
	cmd = exec.CommandContext(ctx, bin, "tunnel", "route", "dns", created.ID, hostname)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		return cloudflare.Zone{}, cloudflare.Tunnel{}, hostname, fmt.Errorf("create Cloudflare DNS route: %w: %s", err, strings.TrimSpace(string(out)))
	}
	completed = true
	return cloudflare.Zone{Name: zoneFromHostname(hostname), AccountID: v.AccountTag}, cloudflare.Tunnel{ID: created.ID, Name: created.Name, AccountID: v.AccountTag, Secret: v.TunnelSecret}, hostname, nil
}

func confirmMCPSetup(hostname, listen, authorization string) (bool, error) {
	desc := strings.Join([]string{
		"Public URL       https://" + hostname + "/mcp",
		"Local service    " + listen + " (loopback only)",
		"Authentication   Bearer token",
		"Cloudflare auth  " + authorization,
		"Server changes   no web-server, TLS or firewall changes",
	}, "\n")
	return ui.Confirm("Create this MCP endpoint?", desc)
}

func promptCloudflareAccountID() (string, error) {
	id, err := ui.Prompt("Cloudflare Account ID", "Cloudflare dashboard → account Overview. Copy the 32-character Account ID shown on the right.", Config().MCP.Cloudflare.AccountID)
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(id)
	if !cloudflareAccountID.MatchString(id) {
		return "", fmt.Errorf("cloudflare account ID must contain exactly 32 hexadecimal characters")
	}
	return strings.ToLower(id), nil
}

func zonesForAccount(zones []cloudflare.Zone, accountID string) []cloudflare.Zone {
	filtered := zones[:0]
	for _, zone := range zones {
		if strings.EqualFold(zone.AccountID, accountID) {
			filtered = append(filtered, zone)
		}
	}
	return filtered
}

func promptMCPHostname(zone string) (string, error) {
	name := "mcp-" + hostnamePart.ReplaceAllString(strings.ToLower(mcpServerHostname()), "-")
	part, err := ui.Prompt("MCP subdomain", "The public URL will be https://<subdomain>."+zone+"/mcp.", name)
	if err != nil {
		return "", err
	}
	part = strings.Trim(strings.ToLower(strings.TrimSpace(part)), "-.")
	if part == "" || strings.Contains(part, ".") {
		return "", fmt.Errorf("subdomain must be one DNS label without dots")
	}
	return part + "." + zone, nil
}

func availableMCPlisten(first, last int) (string, error) {
	for port := first; port <= last; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			_ = ln.Close()
			return addr, nil
		}
	}
	return "", fmt.Errorf("no free MCP port in 127.0.0.1:%d-%d", first, last)
}

func defaultTunnelName() string {
	return "rec-deploy-" + hostnamePart.ReplaceAllString(strings.ToLower(mcpServerHostname()), "-")
}
func mcpServerHostname() string {
	h, _ := os.Hostname()
	if h == "" {
		return "server"
	}
	return h
}
func zoneFromHostname(host string) string {
	p := strings.Split(host, ".")
	if len(p) < 2 {
		return ""
	}
	return strings.Join(p[len(p)-2:], ".")
}

func waitMCPPublic(ctx context.Context, endpoint, token string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, e := http.DefaultClient.Do(req)
		if e == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				if token == "" {
					return nil
				}
				return verifyMCPPublic(ctx, endpoint, token)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("public endpoint did not become reachable")
}

func verifyMCPPublic(ctx context.Context, endpoint, token string) error {
	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"rec-deploy-check","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}
	for i, body := range requests {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if i > 0 {
			req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("MCP verification returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		if i == 1 && !strings.Contains(string(b), "list_repositories") {
			return fmt.Errorf("MCP tools/list response did not contain rec-deploy tools")
		}
	}
	return nil
}
