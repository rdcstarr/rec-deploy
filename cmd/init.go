package cmd

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/notify"
	"github.com/rdcstarr/rec-deploy/internal/systemd"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// defaultListen is the address the daemon binds when nothing is configured. It
// mirrors the config default; the wizard shows it as the pre-filled answer.
const defaultListen = "0.0.0.0:9000"

// updateTimer is the systemd timer that runs `rec-deploy self-update --restart`.
const updateTimer = "rec-deploy-update.timer"

// newInitCmd builds `init`: the interactive setup wizard.
func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "init",
		Short:   "Set rec-deploy up: token, listen address, discovery roots, notifications",
		Long:    "init walks through rec-deploy's configuration, validates the GitHub token against the API, then creates the state database and pins github.com's host keys.",
		Args:    cobra.NoArgs,
		Example: "  rec-deploy init",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isInteractive() {
				return fmt.Errorf("init is interactive — set the values with `rec-deploy config set <key> <value>`")
			}

			return initWizard(cmd.Context())
		},
	}
}

// initialized reports whether the setup wizard has completed on this server. It
// tolerates a nil config so the hub can call it before any command has loaded
// one.
func initialized() bool {
	return cfg != nil && cfg.Initialized
}

// initWizard collects every setting in order, saving as it goes, and finishes by
// creating the state the daemon and `repo add` expect to already exist.
func initWizard(ctx context.Context) error {
	cfg := Config()

	ui.Title("rec-deploy init")

	login, err := initToken(ctx, cfg)
	if err != nil {
		return err
	}
	err = ui.RunWizard(
		ui.WizardStep{Name: "server", Run: func() error { return initServer(cfg) }},
		ui.WizardStep{Name: "discovery", Run: func() error { return initDiscovery(cfg) }},
		ui.WizardStep{Name: "save", Run: save},
		// MCP needs the database before its service can start, and belongs before
		// optional notifications and updates in the operator-facing flow.
		ui.WizardStep{Name: "state", Run: func() error { return initState(ctx) }},
		ui.WizardStep{Name: "mcp", Run: func() error { return initMCP(ctx, cfg) }},
		ui.WizardStep{Name: "notifications", Run: func() error { return initNotify(ctx) }},
		ui.WizardStep{Name: "auto-update", Run: func() error { return initAutoUpdate(ctx) }},
	)
	if err != nil {
		return err
	}

	// Every step ran without an error, which is the whole meaning of the flag: a
	// wizard abandoned with Esc or stopped by a failing step leaves it false, and
	// the hub keeps offering setup.
	cfg.Initialized = true
	if err := save(); err != nil {
		return err
	}

	return initSummary(login)
}

// initMCP optionally enables the daemon's authenticated remote MCP listener. It
// is a no-op on a server that already has one: install.sh runs init on upgrades
// too, and enableCloudflareMCP refuses to provision a replacement — an error
// there would abandon notifications, auto-update and the summary with it.
// Changing an existing endpoint is what `rec-deploy mcp` is for.
func initMCP(ctx context.Context, cfg *config.Config) error {
	if cfg.MCP.Enabled {
		ui.Info("remote MCP is already enabled at " + mcpEndpoint(cfg) + " — change it with `rec-deploy mcp`")

		return nil
	}

	on, err := ui.Confirm("Enable remote read-only MCP access?", "Creates an isolated Cloudflare Tunnel with public HTTPS. Hestia, Nginx, Apache and the firewall are not modified.")
	if err != nil {
		return err
	}
	if !on {
		ui.Info("remote MCP is off — enable it later with `rec-deploy mcp enable`")

		return nil
	}

	return enableCloudflareMCP(ctx)
}

// initToken reads the GitHub token, validates it against GET /user, and refuses
// a token that cannot do the job — naming exactly which scope is missing. An
// an existing token opens pre-filled and masked, so a re-run can inspect or
// replace it without re-typing it.
func initToken(ctx context.Context, cfg *config.Config) (string, error) {
	token, err := ui.SecretPrompt("GitHub token (scopes: repo, admin:repo_hook)", "Create a classic token at github.com/settings/tokens → \"Generate new token (classic)\". Alt+R reveals or masks the stored value.", cfg.GitHub.Token)
	if err != nil {
		return "", err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("a github token is required — create one with the `repo` and `admin:repo_hook` scopes at https://github.com/settings/tokens")
	}

	var u github.User
	if err := ui.Spinner("Validating the token…", func() error {
		var err error
		u, err = github.New(token).User(ctx)

		return err
	}); err != nil {
		return "", err
	}

	if missing := github.MissingScopes(u.Scopes); len(missing) > 0 {
		return "", missingScopesError(missing)
	}

	ui.Success("authenticated as " + u.Login)
	cfg.GitHub.Token = token

	return u.Login, nil
}

// missingScopesError names exactly which required scopes the token lacks. A
// token that cannot create a webhook is otherwise only discovered when the first
// `repo add` fails halfway through, having already uploaded a deploy key.
func missingScopesError(missing []string) error {
	noun := "scope"
	if len(missing) > 1 {
		noun = "scopes"
	}

	return fmt.Errorf("token is missing the %s %s — regenerate it at https://github.com/settings/tokens",
		strings.Join(missing, " and "), noun)
}

// initServer collects the listen address and the URL GitHub delivers to.
func initServer(cfg *config.Config) error {
	listen, err := ui.Prompt("Listen address (host:port)", "Local bind — which of this machine's interfaces accept connections; GitHub never sees this value. 0.0.0.0 means all interfaces (right for most servers). Use 127.0.0.1 only behind a local reverse proxy.", orDefault(cfg.Listen, defaultListen))
	if err != nil {
		return err
	}
	listen = orDefault(strings.TrimSpace(listen), defaultListen)
	if err := validateConfigValue("listen", listen); err != nil {
		return err
	}
	cfg.Listen = listen

	// State the trade-off plainly rather than let it be discovered: the payload
	// (repository, branch, commit message) travels in clear, and no credential is
	// in it. The HMAC signature over the raw body is what prevents forgery.
	ui.Info("GitHub posts the payload over plain http — repository, branch, commit message.")
	ui.Info("No credential crosses the network: the HMAC signature is what prevents forgery.")

	publicURL, err := ui.Prompt("Public URL GitHub delivers to (e.g. http://1.2.3.4:9000)", "Registered with GitHub as the webhook destination. Must be reachable from the internet — open the port in the firewall. Usually http://<public-ip>:<port>.",
		orDefault(cfg.PublicURL, defaultPublicURL(cfg.Listen)))
	if err != nil {
		return err
	}
	publicURL = strings.TrimSpace(publicURL)
	if err := validateConfigValue("public_url", publicURL); err != nil {
		return err
	}
	cfg.PublicURL = publicURL

	return nil
}

// defaultPublicURL proposes the URL GitHub delivers to: this host's routed
// address on the port the daemon listens on.
func defaultPublicURL(listen string) string {
	return publicURLFor(outboundIP(), listen)
}

// outboundIP returns the local address the kernel would route to the internet
// from, or "" when there is no route. UDP is connectionless, so the Dial sends
// nothing and reaches nothing — it only asks the routing table which source
// address it would pick. That keeps the guess free of an external service.
func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}

	return addr.IP.String()
}

// publicURLFor builds the webhook URL for address ip and the port of the listen
// address. With no address there is nothing to propose; with an unreadable
// listen address the default port is still a better guess than none.
func publicURLFor(ip, listen string) string {
	if ip == "" {
		return ""
	}

	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		_, port, _ = net.SplitHostPort(defaultListen)
	}

	return "http://" + net.JoinHostPort(ip, port)
}

// initDiscovery collects the roots the scan walks. An empty answer keeps the
// current roots: a deploy with none finds nothing, which is an error every time.
func initDiscovery(cfg *config.Config) error {
	roots, err := ui.Prompt("Scan roots (comma-separated globs)", "Directories walked to find checkouts of registered repositories, e.g. /home/*/web,/var/www. Empty keeps the current roots — with none configured every deploy fails.", strings.Join(cfg.Discovery.Roots, ","))
	if err != nil {
		return err
	}
	if err := validateConfigValue("discovery.roots", roots); err != nil {
		return err
	}
	if list := splitList(roots); len(list) > 0 {
		cfg.Discovery.Roots = list
	}

	return nil
}

// initNotify offers each notification channel in turn: a yes/no gate, and when
// the operator says yes, its configurator runs right away. A single MultiSelect
// used to stand here, but its Space-to-toggle was a footgun — operators
// highlighted a channel, pressed Enter, and submitted an empty selection the
// wizard then silently skipped. A per-channel confirm reads the natural way:
// answer yes and you land in that channel's configuration.
func initNotify(ctx context.Context) error {
	telegram, err := ui.Confirm("Enable Telegram notifications?", "Sends deploy results to a Telegram chat through a bot. Answer yes to enter the bot token and chat ID next.")
	if err != nil {
		return err
	}
	if telegram {
		if err := configureTelegram(); err != nil {
			return err
		}
	}

	email, err := ui.Confirm("Enable email notifications?", "Sends deploy results by email over SMTP. Answer yes to enter the server, sender, recipient and credentials next.")
	if err != nil {
		return err
	}
	if email {
		if err := configureEmail(ctx); err != nil {
			return err
		}
	}

	// Declining both channels used to leave the step silent — say what happened so
	// an operator who ends up with no notifications knows where to add them later.
	// A partially filled channel is not this case: its own configurator already
	// warned it stays disabled, and offerTestNotification speaks for a full one.
	cfg := Config()
	if !telegram && !email && !cfg.Notify.Telegram.Configured() && !cfg.Notify.Email.Configured() {
		ui.Info("no notification channels selected — configure later with:  rec-deploy config")
	}

	return offerTestNotification(ctx)
}

// offerTestNotification sends one summary through the configured channels on
// request. A wrong chat ID or SMTP password is worth finding here, not on the
// deploy whose outcome the notification was supposed to carry.
func offerTestNotification(ctx context.Context) error {
	cfg := Config()
	if !cfg.Notify.Telegram.Configured() && !cfg.Notify.Email.Configured() {
		return nil
	}

	send, err := ui.Confirm("Send a test notification now?", "Sends a summary through every configured channel — a wrong chat ID or SMTP password is cheaper to find now than on a real deploy.")
	if err != nil {
		return err
	}
	if !send {
		return nil
	}

	var results []notify.ChannelResult
	if err := ui.Spinner("Sending the test notification…", func() error {
		results = notify.Deliver(ctx, cfg.Notify, notify.Summary{
			Repository: "rec-deploy",
			Ref:        "refs/heads/main",
			Status:     "test",
			Message:    "Notifications are configured correctly.",
		})

		return nil
	}); err != nil {
		return err
	}

	// A failed channel is named above by printChannelResults; it does not abort
	// the wizard — the rest of init (state, host keys) does not depend on it,
	// and the operator can fix the channel afterwards with `rec-deploy config`.
	printChannelResults(results)

	return nil
}

// initAutoUpdate offers to enable the update timer. It is opt-in and stays that
// way: a root daemon that executes code from remote repositories and silently
// replaces its own binary is a security decision, and it is the operator's.
//
// There is deliberately no config.yaml key for this. systemd's enablement state
// is the single source of truth, so the file and the machine cannot disagree.
func initAutoUpdate(ctx context.Context) error {
	if !systemd.Available() {
		return nil
	}

	on, err := ui.Confirm("Keep rec-deploy up to date automatically? (checks hourly for a new release)", "Enables a systemd timer that checks GitHub releases hourly, verifies the checksum, swaps the binary and restarts the daemon. Disable later with:  systemctl disable --now rec-deploy-update.timer")
	if err != nil {
		return err
	}
	if !on {
		// Declining does not turn an existing timer off, so saying "auto-update is
		// off" on a re-run would contradict the summary two screens later.
		if systemd.IsEnabled(ctx, updateTimer) {
			ui.Info("auto-update stays on — turn it off with `systemctl disable --now " + updateTimer + "`")

			return nil
		}

		ui.Info("auto-update is off — enable it later with `systemctl enable --now " + updateTimer + "`")

		return nil
	}

	if err := systemd.EnableNow(ctx, updateTimer); err != nil {
		// A warning, not an error. Everything that matters is already configured by
		// now, and failing here would throw that away over an opt-in extra — most
		// often because the timer unit is not on disk at all, which no amount of
		// retrying inside init can mend.
		ui.Warn("could not enable auto-update: " + err.Error() + " — the rest of the setup is done; enable it later with `systemctl enable --now " + updateTimer + "`")

		return nil
	}

	ui.Success("auto-update on — a new release is picked up within the hour, and rolled back if it will not start")

	return nil
}

// initState creates the state directory, runs the migrations, and pins
// github.com's host keys, so the first `repo add` or push finds them ready
// rather than racing to build them.
func initState(ctx context.Context) error {
	if err := ui.Spinner("Creating the state database…", func() error {
		st, err := openStore(ctx)
		if err != nil {
			return err
		}

		return st.Close()
	}); err != nil {
		return err
	}

	return ui.Spinner("Pinning github.com host keys…", func() error {
		return pinHostKeys(ctx)
	})
}

// initSummary reports what the wizard wrote and where, and points at the one
// command that follows it.
func initSummary(login string) error {
	cfg := Config()

	path, err := configPath()
	if err != nil {
		return err
	}
	stateDir, err := config.StateDir()
	if err != nil {
		return err
	}

	if flagJSON {
		return ui.PrintJSON(map[string]any{
			"config":      path,
			"state":       stateDir,
			"login":       login,
			"token":       tokenState(cfg.GitHub.Token),
			"listen":      cfg.Listen,
			"public_url":  cfg.PublicURL,
			"roots":       cfg.Discovery.Roots,
			"telegram":    tokenState(cfg.Notify.Telegram.Token),
			"email":       tokenState(cfg.Notify.Email.SMTP),
			"auto_update": systemd.IsEnabled(context.Background(), updateTimer),
		})
	}

	ui.Out("")
	ui.Success("rec-deploy is set up")
	ui.KeyValue("account", login)
	ui.KeyValue("config", path)
	ui.KeyValue("state", stateDir)
	ui.KeyValue("auto-update", onOff(systemd.IsEnabled(context.Background(), updateTimer)))
	ui.KeyValue("listen", cfg.Listen)
	ui.KeyValue("public_url", orNotSet(cfg.PublicURL))
	ui.Info("next: run `rec-deploy repo add owner/repo` to register a repository")

	return nil
}

// orDefault returns s, or fallback when s is empty.
func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}

	return s
}

// onOff renders a boolean the way the summary and the status view print it.
func onOff(b bool) string {
	if b {
		return "on"
	}

	return "off"
}
