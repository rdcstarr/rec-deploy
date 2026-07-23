package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/cloudflare"
	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/mcpserver"
	"github.com/rdcstarr/rec-deploy/internal/store"
	"github.com/rdcstarr/rec-deploy/internal/systemd"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newMCPCmd builds `mcp`: manage remote MCP or serve MCP over stdio for local clients.
func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mcp",
		Short:   "Manage or serve read-only rec-deploy tools over MCP",
		Long:    "mcp serves read-only repository, installation, deploy, manifest and status tools. A client can launch the bare command over stdio; management subcommands control the remote Streamable HTTP endpoint hosted by `rec-deploy serve`.",
		Example: `Configure a local MCP client with command "/usr/bin/rec-deploy" and args ["mcp"].`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if isInteractive() {
				return mcpMenu(cmd)
			}

			path, err := config.StateDB()
			if err != nil {
				return err
			}
			st, err := store.OpenReadOnly(cmd.Context(), path)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			return mcpserver.New(Config(), st).RunStdio(cmd.Context())
		},
	}

	cmd.AddCommand(newMCPEnableCmd(), newMCPDisableCmd(), newMCPStatusCmd(), newMCPClientConfigCmd(), newMCPShowTokenCmd(), newMCPCloudflareCredentialsCmd(), newMCPTokenCmd(), newMCPServeHTTPCmd(), newMCPDependencyCmd())
	return cmd
}

// mcpMenu is the interactive hub for remote MCP administration.
func mcpMenu(cmd *cobra.Command) error {
	return (ui.Menu{
		Title:   ui.ScreenPath("rec-deploy", "MCP"),
		Options: func() []ui.Option { return mcpMenuOptions(Config()) },
		Help:    func() string { return commandHelp(cmd) },
		Handle:  func(choice string) error { return dispatch(cmd, choice) },
	}).Run()
}

func mcpMenuOptions(cfg *config.Config) []ui.Option {
	items := []ui.DescribedOption{
		{Name: "Status", Description: onOff(cfg.MCP.Enabled) + " · " + cfg.MCP.Listen, Value: "status"},
	}
	if cfg.MCP.Enabled {
		items = append(items,
			ui.DescribedOption{Name: "Client JSON", Description: "ready to copy into an MCP client", Value: "client-config"},
			ui.DescribedOption{Name: "Bearer token", Description: "show the token used by MCP clients", Value: "show-token"},
			ui.DescribedOption{Name: "Cloudflare", Description: cloudflareCredentialSummary(cfg.MCP.Cloudflare), Value: "cloudflare"},
			ui.DescribedOption{Name: "Disable", Description: "remove public remote access", Value: "disable"},
		)
	} else {
		items = append(items,
			ui.DescribedOption{Name: "Enable", Description: "guided Cloudflare HTTPS setup", Value: "enable"},
			ui.DescribedOption{Name: "Cloudflare", Description: cloudflareCredentialSummary(cfg.MCP.Cloudflare), Value: "cloudflare"},
			ui.DescribedOption{Name: "MCP bearer", Description: mcpTokenSummary(cfg.MCP.TokenHash), Value: "token"},
		)
	}
	return ui.DescribedOptions(items...)
}

func cloudflareCredentialSummary(cfg config.CloudflareConfig) string {
	if cfg.AccountID == "" && cfg.APIToken == "" {
		return "credentials not configured"
	}
	if cfg.AccountID == "" || cfg.APIToken == "" {
		return "incomplete credentials"
	}
	return "Account ID and API token configured"
}

func newMCPCloudflareCredentialsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cloudflare",
		Short: "Edit the Cloudflare Account ID and API token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isInteractive() {
				return fmt.Errorf("cloudflare credentials are secret — edit them from `sudo rec-deploy mcp` in a terminal")
			}
			cfg := Config()
			accountID := cfg.MCP.Cloudflare.AccountID
			apiToken := cfg.MCP.Cloudflare.APIToken
			if err := ui.Form([]ui.Field{
				{Title: "Cloudflare Account ID", Desc: "Cloudflare dashboard → account Overview.", Value: &accountID},
				{Title: "Cloudflare Account API token", Desc: "Stored root-only in config.yaml. Alt+R reveals or masks it.", Secret: true, Value: &apiToken},
			}); err != nil {
				return err
			}
			accountID = strings.TrimSpace(accountID)
			apiToken = strings.TrimSpace(apiToken)
			if !cloudflareAccountID.MatchString(accountID) {
				return fmt.Errorf("cloudflare account ID must contain exactly 32 hexadecimal characters")
			}
			if apiToken == "" {
				return fmt.Errorf("cloudflare API token is required")
			}
			cfg.MCP.Cloudflare.AccountID = accountID
			cfg.MCP.Cloudflare.APIToken = apiToken
			if err := save(); err != nil {
				return err
			}
			return nil
		},
	}
}

func newMCPClientConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "client-config",
		Short: "Show ready-to-use JSON for an MCP client",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := Config()
			if !cfg.MCP.Enabled || cfg.MCP.PublicURL == "" {
				return fmt.Errorf("remote MCP is not enabled — run `rec-deploy mcp enable`")
			}
			token, err := readMCPToken(cfg.MCP.TokenHash)
			if err != nil {
				return fmt.Errorf("the bearer token is unavailable — rotate it from rec-deploy → MCP → MCP bearer: %w", err)
			}
			body, err := mcpClientJSON(cfg.MCP.PublicURL, token)
			if err != nil {
				return err
			}
			if !isInteractive() {
				ui.Out(body)
				return nil
			}
			err = (ui.Document{
				Title: ui.ScreenPath("rec-deploy", "MCP", "Client JSON"),
				Body:  body,
			}).Run()
			if errors.Is(err, ui.ErrBack) {
				return nil
			}
			return err
		},
	}
}

func mcpClientJSON(endpoint, token string) (string, error) {
	config := map[string]any{
		"mcpServers": map[string]any{
			"rec-deploy": map[string]any{
				"type": "http",
				"url":  endpoint,
				"headers": map[string]string{
					"Authorization": "Bearer " + token,
				},
			},
		},
	}
	b, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func newMCPShowTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "show-token",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := readMCPToken(Config().MCP.TokenHash)
			if err != nil {
				return fmt.Errorf("the bearer token is unavailable — rotate it with `rec-deploy mcp token rotate`: %w", err)
			}
			if !isInteractive() {
				ui.Out(token)
				return nil
			}
			err = (ui.Report{
				Title: ui.ScreenPath("rec-deploy", "MCP", "Bearer token"),
				Rows:  [][2]string{{"token", token}},
			}).Run()
			if errors.Is(err, ui.ErrBack) {
				return nil
			}
			return err
		},
	}
}

func newMCPEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "Enable remote MCP through an isolated Cloudflare Tunnel",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isInteractive() {
				return fmt.Errorf("cloudflare MCP setup is interactive — run `sudo rec-deploy mcp enable` in a terminal")
			}
			return enableCloudflareMCP(cmd.Context())
		},
	}
}

func newMCPDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Disable the remote MCP endpoint",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := Config()
			if cfg.MCP.Mode == "cloudflare" {
				return disableCloudflareMCP(cmd.Context())
			}
			cfg.MCP.Enabled = false
			if err := save(); err != nil {
				return err
			}
			ui.Info("restart the daemon to apply:  systemctl restart rec-deploy")
			return nil
		},
	}
}

func newMCPStatusCmd() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show remote MCP configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if isInteractive() && !flagJSON {
			return mcpStatusView()
		}
		return printMCPStatus()
	}}
}

func mcpStatusView() error {
	cfg := Config()
	return (ui.Report{
		Title: ui.ScreenPath("rec-deploy", "MCP", "Status"),
		Rows:  mcpStatusRows(cfg),
	}).Run()
}

func mcpStatusRows(cfg *config.Config) [][2]string {
	return [][2]string{
		{"remote", onOff(cfg.MCP.Enabled)},
		{"mode", orDefault(cfg.MCP.Mode, "legacy-http")},
		{"listen", cfg.MCP.Listen},
		{"endpoint", mcpEndpoint(cfg)},
		{"token", tokenState(cfg.MCP.TokenHash)},
		{"MCP service", serviceState(mcpService)},
		{"Cloudflare tunnel", serviceState(mcpTunnelService)},
		{"public HTTPS", mcpPublicState(cfg)},
	}
}

func newMCPTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage the remote MCP bearer token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isInteractive() {
				return cmd.Help()
			}
			return mcpTokenMenu(cmd)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "rotate",
		Short: "Replace and provision the remote MCP bearer token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if isInteractive() {
				yes, err := ui.Confirm("Rotate the MCP token?", "The current token stops working after the daemon restarts. Every remote client must be updated with the new token.")
				if err != nil {
					return err
				}
				if !yes {
					return ui.ErrBack
				}
			} else if !flagYes {
				return fmt.Errorf("token rotation requires confirmation — re-run with `rec-deploy mcp token rotate --yes`")
			}
			token, hash, err := mcpserver.NewToken()
			if err != nil {
				return err
			}
			Config().MCP.TokenHash = hash
			if err := save(); err != nil {
				return err
			}
			printMCPConnection(token)
			if Config().MCP.Mode == "cloudflare" && systemd.IsActive(cmd.Context(), mcpService) {
				if err := systemd.Restart(cmd.Context(), mcpService); err != nil {
					return err
				}
				ui.Info("the previous token is now invalid")
			} else {
				ui.Info("the previous token is invalid after `systemctl restart rec-deploy`")
			}
			return nil
		},
	})
	return cmd
}

func mcpTokenMenu(cmd *cobra.Command) error {
	for {
		menuHelp = commandHelp(cmd)
		token, _ := readMCPToken(Config().MCP.TokenHash)
		state := "provisioning file unavailable · rotate to create a new token"
		if token != "" {
			state = redact(token) + " · open to reveal"
		}
		picker := ui.Picker{
			Title: ui.ScreenPath("rec-deploy", "MCP", "Token"),
			Options: ui.DescribedOptions(
				ui.DescribedOption{Name: "View token", Description: state, Value: "view"},
				ui.DescribedOption{Name: "Rotate", Description: "invalidate the old token and create a new one", Value: "rotate"},
			),
			Help: commandHelp(cmd),
		}
		res, err := picker.Run()
		if err != nil {
			return err
		}
		if res.Value == "" {
			return ui.ErrBack
		}
		if res.Value == "rotate" {
			return dispatch(cmd, "rotate")
		}
		if token == "" {
			ui.Warn("the clear token is no longer available — rotate it to provision a new one")
			continue
		}
		if err := (ui.SecretDetail{
			Title: ui.ScreenPath("rec-deploy", "MCP", "Access token"),
			Label: "bearer token",
			Value: token,
		}).Run(); err != nil && !errors.Is(err, ui.ErrBack) {
			return err
		}
	}
}

func printMCPStatus() error {
	cfg := Config()
	if flagJSON {
		return ui.PrintJSON(map[string]any{"enabled": cfg.MCP.Enabled, "mode": cfg.MCP.Mode, "listen": cfg.MCP.Listen, "endpoint": mcpEndpoint(cfg), "token": tokenState(cfg.MCP.TokenHash), "mcp_service": serviceState(mcpService), "tunnel_service": serviceState(mcpTunnelService), "public_https": mcpPublicState(cfg)})
	}
	(ui.Report{Title: ui.ScreenPath("rec-deploy", "MCP"), Rows: mcpStatusRows(cfg)}).Print()
	return nil
}

func printMCPConnection(token string) {
	cfg := Config()
	ui.Success("remote MCP enabled at " + mcpEndpoint(cfg))
	if token == "" {
		ui.Info("the existing token remains active; rotate it if the client no longer has it")
		return
	}
	path, err := writeMCPToken(token)
	if err != nil {
		ui.Warn("could not write the one-time token file: " + err.Error())
		return
	}
	ui.Warn("copy the token from this root-only file, then delete the file; rec-deploy stores only its hash")
	ui.KeyValue("token file", path)
	ui.Info("OpenClaw URL:  " + mcpEndpoint(cfg))
}

func writeMCPToken(token string) (string, error) {
	path, err := mcpTokenPath()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func readMCPToken(expectedHash string) (string, error) {
	path, err := mcpTokenPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" || mcpserver.TokenHash(token) != expectedHash {
		return "", fmt.Errorf("provisioning token does not match the configured token")
	}
	return token, nil
}

func mcpTokenSummary(tokenHash string) string {
	if tokenHash == "" {
		return "not created"
	}
	if token, _ := readMCPToken(tokenHash); token != "" {
		return "configured · reveal available"
	}
	return "configured · rotate to reveal"
}

func mcpTokenPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mcp-token"), nil
}

func mcpEndpoint(cfg *config.Config) string {
	if cfg.MCP.PublicURL != "" {
		return cfg.MCP.PublicURL
	}
	host := ""
	if public, err := url.Parse(cfg.PublicURL); err == nil {
		host = public.Hostname()
	}
	if host == "" {
		listenHost, _, err := net.SplitHostPort(cfg.MCP.Listen)
		if err == nil && listenHost != "" && listenHost != "0.0.0.0" && listenHost != "::" {
			host = listenHost
		}
	}
	if host == "" {
		host = outboundIP()
	}
	if host == "" {
		host = "<server-ip>"
	}
	return "http://" + net.JoinHostPort(host, portOf(cfg.MCP.Listen)) + "/mcp"
}

func newMCPServeHTTPCmd() *cobra.Command {
	return &cobra.Command{Use: "serve-http", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg := Config()
		if cfg.MCP.Mode != "cloudflare" || cfg.MCP.TokenHash == "" {
			return fmt.Errorf("cloudflare MCP is not configured — run `rec-deploy mcp enable`")
		}
		path, err := config.StateDB()
		if err != nil {
			return err
		}
		st, err := store.OpenReadOnly(cmd.Context(), path)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		mcpHandler := mcpserver.New(cfg, st).HTTPHandler(cfg.MCP.TokenHash)
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			mcpHandler.ServeHTTP(w, r)
		})
		srv := &http.Server{Addr: cfg.MCP.Listen, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 32 << 10}
		errs := make(chan error, 1)
		go func() { errs <- srv.ListenAndServe() }()
		select {
		case err := <-errs:
			return err
		case <-cmd.Context().Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return srv.Shutdown(shutdown)
		}
	}}
}

func newMCPDependencyCmd() *cobra.Command {
	dep := &cobra.Command{Use: "dependency", Hidden: true}
	dep.AddCommand(&cobra.Command{Use: "update", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		path, err := config.CloudflaredBinary()
		if err != nil {
			return err
		}
		backup := path + ".rollback"
		hadOld := false
		if _, statErr := os.Stat(path); statErr == nil {
			_ = os.Remove(backup)
			if err := os.Rename(path, backup); err != nil {
				return fmt.Errorf("stage cloudflared update: %w", err)
			}
			hadOld = true
		}
		_, err = cloudflare.InstallLatest(cmd.Context(), path)
		if err != nil {
			if hadOld {
				_ = os.Rename(backup, path)
			}
			return err
		}
		if systemd.IsActive(cmd.Context(), mcpTunnelService) {
			if err := systemd.Restart(cmd.Context(), mcpTunnelService); err != nil {
				_ = os.Remove(path)
				if hadOld {
					_ = os.Rename(backup, path)
					_ = systemd.Restart(context.Background(), mcpTunnelService)
				}
				return fmt.Errorf("restart tunnel after cloudflared update: %w", err)
			}
		}
		_ = os.Remove(backup)
		return nil
	}})
	return dep
}

func portOf(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	return port
}

func serviceState(name string) string {
	if !systemd.Available() {
		return "unavailable"
	}
	if systemd.IsActive(context.Background(), name) {
		return "active"
	}
	return "inactive"
}

func mcpPublicState(cfg *config.Config) string {
	if !cfg.MCP.Enabled || cfg.MCP.Mode != "cloudflare" || cfg.MCP.PublicURL == "" {
		return "disabled"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.MCP.PublicURL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		return "invalid URL"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "unreachable"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return "ready"
	}
	return fmt.Sprintf("HTTP %d", resp.StatusCode)
}
