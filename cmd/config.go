package cmd

import (
	"context"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/notify"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newConfigCmd builds the `config` command: an interactive configuration form,
// plus non-interactive path/get/set helpers for scripting and non-TTY use.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configure rec-deploy interactively, or get/set values",
		Args:  cobra.NoArgs,
		Example: `  rec-deploy config
  rec-deploy config path
  rec-deploy config get listen
  rec-deploy config set listen 0.0.0.0:9000
  rec-deploy config set discovery.roots /var/www,/home/*/web/*/public_html`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if isInteractive() {
				return configMenu(cmd.Context())
			}

			return printConfig()
		},
	}

	cmd.AddCommand(newConfigPathCmd(), newConfigGetCmd(), newConfigSetCmd())

	return cmd
}

// configMenu is the interactive section picker: it loops until the user backs
// out, running the chosen section's form and saving what it collected.
func configMenu(ctx context.Context) error {
	return (ui.Menu{
		Title:      ui.ScreenPath("rec-deploy", "Config"),
		Options:    configMenuOptions,
		Help:       func() string { return menuHelp },
		BackValues: map[string]bool{"exit": true},
		Handle:     func(section string) error { return openConfigSection(ctx, section) },
	}).Run()
}

// configMenuOptions lists the sections, each described by what it configures.
// It is built from configSections rather than hand-listed, so a new section
// cannot appear in `config get` and be missing from the menu.
func configMenuOptions() []ui.Option {
	items := make([]ui.DescribedOption, 0, len(configSections))
	for _, section := range configSections {
		items = append(items, ui.DescribedOption{Name: section.Title, Description: section.Description, Value: section.Key})
	}

	return append(ui.DescribedOptions(items...), ui.Option{Label: "Exit", Value: "exit"})
}

// openConfigSection opens a scoped overview before editing one setting. Secret
// values stay masked here and can be revealed only inside their own editor.
func openConfigSection(ctx context.Context, section string) error {
	return (ui.Menu{
		Title:      ui.ScreenPath("rec-deploy", "Config", configSectionTitle(section)),
		Options:    func() []ui.Option { return configSectionOptions(section) },
		SelectHelp: "edit setting",
		BackValues: map[string]bool{"back": true},
		Handle:     func(key string) error { return configureConfigField(ctx, key) },
	}).Run()
}

// configSection is one group of settings, as the config menu presents it. The
// description says what the section is for; the values live one level down,
// inside the section, where they are being edited.
type configSection struct {
	Key         string
	Title       string
	Description string
}

var configSections = []configSection{
	{Key: "server", Title: "Server", Description: "where the daemon listens and the URL GitHub posts to"},
	{Key: "github", Title: "GitHub", Description: "the token that manages deploy keys and webhooks"},
	{Key: "discovery", Title: "Discovery", Description: "where checkouts are looked for on this server"},
	{Key: "telegram", Title: "Telegram", Description: "send deploy results to a Telegram chat"},
	{Key: "email", Title: "Email", Description: "send deploy results by email"},
}

// configField is the single source of truth for every configurable value. The
// interactive menu and editor, config get/set, secret handling and known-key
// errors are all derived from this registry.
type configField struct {
	Key, Section, Label string
	Title, Description  string
	Secret              bool
	Get                 func(*config.Config) string
	Set                 func(*config.Config, string)
	Display             func(*config.Config) string
	Validate            func(string) error
}

func (f configField) display(cfg *config.Config) string {
	if f.Display != nil {
		return f.Display(cfg)
	}
	if f.Secret {
		return redact(f.Get(cfg))
	}
	return orNotSet(f.Get(cfg))
}

var configFields = []configField{
	{Key: "listen", Section: "server", Label: "Listen", Title: "Listen address", Description: "Local bind in host:port form, usually 0.0.0.0:9000.", Get: func(c *config.Config) string { return c.Listen }, Set: func(c *config.Config, v string) { c.Listen = v }, Validate: func(v string) error { return validateEndpoint("listen", v, false) }},
	{Key: "public_url", Section: "server", Label: "Public URL", Title: "Public URL", Description: "HTTP(S) origin GitHub can reach, for example https://deploy.example.com.", Get: func(c *config.Config) string { return c.PublicURL }, Set: func(c *config.Config, v string) { c.PublicURL = v }, Validate: validatePublicURL},
	{Key: "github.token", Section: "github", Label: "Token", Title: "GitHub token", Description: "Classic token with repo and admin:repo_hook scopes.", Secret: true, Get: func(c *config.Config) string { return c.GitHub.Token }, Set: func(c *config.Config, v string) { c.GitHub.Token = v }},
	{Key: "discovery.roots", Section: "discovery", Label: "Roots", Title: "Discovery roots", Description: "Comma-separated directory globs. Empty disables discovery.", Get: func(c *config.Config) string { return strings.Join(c.Discovery.Roots, ",") }, Set: func(c *config.Config, v string) { c.Discovery.Roots = splitList(v) }, Display: func(c *config.Config) string { return listSummary(c.Discovery.Roots) }, Validate: validateDiscoveryRoots},
	{Key: "discovery.prune", Section: "discovery", Label: "Prune", Title: "Pruned directories", Description: "Comma-separated directory names never entered during discovery.", Get: func(c *config.Config) string { return strings.Join(c.Discovery.Prune, ",") }, Set: func(c *config.Config, v string) { c.Discovery.Prune = splitList(v) }, Display: func(c *config.Config) string { return listSummary(c.Discovery.Prune) }, Validate: validateDiscoveryPrune},
	{Key: "notify.telegram.token", Section: "telegram", Label: "Bot token", Title: "Telegram bot token", Description: "Token created by @BotFather.", Secret: true, Get: func(c *config.Config) string { return c.Notify.Telegram.Token }, Set: func(c *config.Config, v string) { c.Notify.Telegram.Token = v }},
	{Key: "notify.telegram.chat_id", Section: "telegram", Label: "Chat ID", Title: "Telegram chat ID", Description: "Numeric user/group ID or @channelusername.", Get: func(c *config.Config) string { return c.Notify.Telegram.ChatID }, Set: func(c *config.Config, v string) { c.Notify.Telegram.ChatID = v }, Validate: validateTelegramChatID},
	{Key: "notify.email.smtp", Section: "email", Label: "SMTP", Title: "SMTP server", Description: "SMTP endpoint in host:port form, for example smtp.example.com:587.", Get: func(c *config.Config) string { return c.Notify.Email.SMTP }, Set: func(c *config.Config, v string) { c.Notify.Email.SMTP = v }, Validate: func(v string) error { return validateEndpoint("notify.email.smtp", v, true) }},
	{Key: "notify.email.from", Section: "email", Label: "From", Title: "From address", Description: "Envelope sender for notification emails.", Get: func(c *config.Config) string { return c.Notify.Email.From }, Set: func(c *config.Config, v string) { c.Notify.Email.From = v }, Validate: func(v string) error { return validateEmailAddress("notify.email.from", v) }},
	{Key: "notify.email.to", Section: "email", Label: "To", Title: "To address", Description: "Recipient of deploy notifications.", Get: func(c *config.Config) string { return c.Notify.Email.To }, Set: func(c *config.Config, v string) { c.Notify.Email.To = v }, Validate: func(v string) error { return validateEmailAddress("notify.email.to", v) }},
	{Key: "notify.email.username", Section: "email", Label: "Username", Title: "SMTP username", Description: "Empty disables SMTP authentication.", Get: func(c *config.Config) string { return c.Notify.Email.Username }, Set: func(c *config.Config, v string) { c.Notify.Email.Username = v }},
	{Key: "notify.email.password", Section: "email", Label: "Password", Title: "SMTP password", Description: "Password used only when an SMTP username is configured.", Secret: true, Get: func(c *config.Config) string { return c.Notify.Email.Password }, Set: func(c *config.Config, v string) { c.Notify.Email.Password = v }},
}

func findConfigField(key string) (configField, bool) {
	for _, field := range configFields {
		if field.Key == key {
			return field, true
		}
	}
	return configField{}, false
}

func configSectionTitle(section string) string {
	for _, item := range configSections {
		if item.Key == section {
			return item.Title
		}
	}
	return section
}

// configSectionOptions returns the current, safely masked values shown in a
// credential section. Selecting any setting opens that section's editor.
func configSectionOptions(section string) []ui.Option {
	cfg := Config()
	var items []ui.DescribedOption
	for _, field := range configFields {
		if field.Section != section {
			continue
		}
		items = append(items, ui.DescribedOption{Name: field.Label, Description: field.display(cfg), Value: field.Key})
	}
	options := ui.DescribedOptions(items...)
	return append(options, ui.Option{Label: "Back", Value: "back"})
}

func listSummary(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}

	return strings.Join(values, ", ")
}

// configureConfigField edits and validates one selected setting. Stored
// credentials open pre-filled but masked; Alt+R reveals them inside the input.
func configureConfigField(ctx context.Context, key string) error {
	cfg := Config()
	current, secret, err := configGet(cfg, key)
	if err != nil {
		return err
	}

	title, desc := configFieldCopy(key)
	var value string
	if secret {
		value, err = ui.SecretPrompt(title, desc, current)
	} else {
		value, err = ui.Prompt(title, desc, current)
	}
	if err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if err := validateConfigValue(key, value); err != nil {
		return err
	}
	if key == "github.token" {
		if err := validateGitHubToken(ctx, value); err != nil {
			return err
		}
	}
	if err := configSet(cfg, key, value); err != nil {
		return err
	}
	if err := save(); err != nil {
		return err
	}

	if key == "listen" || key == "public_url" {
		ui.Info("server address changes apply after `systemctl restart rec-deploy`")
	}
	if strings.HasPrefix(key, "notify.telegram.") && telegramPartial(cfg.Notify.Telegram) {
		ui.Warn("telegram stays disabled until both the bot token and chat id are set")
	}
	if strings.HasPrefix(key, "notify.email.") && emailPartial(cfg.Notify.Email) {
		ui.Warn("email stays disabled until smtp, from and to are all set")
	}

	return nil
}

func configFieldCopy(key string) (title, desc string) {
	if field, ok := findConfigField(key); ok {
		return field.Title, field.Description
	}
	return key, ""
}

func validateConfigValue(key, value string) error {
	field, ok := findConfigField(key)
	if !ok {
		return unknownKey(key)
	}
	if field.Validate != nil {
		return field.Validate(value)
	}
	return nil
}

func validateEndpoint(key, value string, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("%s must be host:port: %w", key, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%s port must be between 1 and 65535", key)
	}
	return nil
}

func validatePublicURL(value string) error {
	if value == "" {
		return nil
	}
	u, err := url.ParseRequestURI(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("public_url must be an absolute http or https URL")
	}
	return nil
}

func validateEmailAddress(key, value string) error {
	if value == "" {
		return nil
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value {
		return fmt.Errorf("%s must be one email address", key)
	}
	return nil
}

func validateDiscoveryRoots(value string) error {
	for _, root := range splitList(value) {
		if _, err := filepath.Glob(root); err != nil {
			return fmt.Errorf("invalid discovery root %q: %w", root, err)
		}
	}
	return nil
}

func validateDiscoveryPrune(value string) error {
	for _, name := range splitList(value) {
		if name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
			return fmt.Errorf("prune entry %q must be a directory name, not a path", name)
		}
	}
	return nil
}

func validateTelegramChatID(value string) error {
	if value == "" || (strings.HasPrefix(value, "@") && len(value) > 1) {
		return nil
	}
	if _, err := strconv.ParseInt(value, 10, 64); err != nil {
		return fmt.Errorf("notify.telegram.chat_id must be numeric or start with @")
	}
	return nil
}

func validateGitHubToken(ctx context.Context, token string) error {
	var user github.User
	if err := ui.Spinner("Validating GitHub token…", func() error {
		var err error
		user, err = github.New(token).User(ctx)

		return err
	}); err != nil {
		return err
	}
	if missing := github.MissingScopes(user.Scopes); len(missing) > 0 {
		return missingScopesError(missing)
	}

	ui.Success("authenticated as " + user.Login)

	return nil
}

// configureTelegram collects the Telegram bot token and chat ID. An existing
// token opens pre-filled and masked, with Alt+R available inside its editor.
func configureTelegram() error {
	cfg := Config()

	token, err := ui.SecretPrompt("Telegram bot token (from @BotFather)", "From @BotFather (/newbot). Alt+R reveals or masks the stored value.", cfg.Notify.Telegram.Token)
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)

	chatID, err := ui.Prompt("Telegram chat / user ID", "Numeric user or group ID (group IDs are negative), or @channelusername for channels. @userinfobot tells you yours.", cfg.Notify.Telegram.ChatID)
	if err != nil {
		return err
	}

	chatID = strings.TrimSpace(chatID)
	if err := validateConfigValue("notify.telegram.chat_id", chatID); err != nil {
		return err
	}
	cfg.Notify.Telegram.Token = token
	cfg.Notify.Telegram.ChatID = chatID

	if err := save(); err != nil {
		return err
	}
	if telegramPartial(cfg.Notify.Telegram) {
		ui.Warn("telegram stays disabled until both the bot token and the chat id are set")
	}

	return nil
}

// configureEmail collects the SMTP notification settings in a single form. The
// password field is pre-filled and masked; Alt+R reveals it in place.
func configureEmail(ctx context.Context) error {
	cfg := Config()
	smtp, from := cfg.Notify.Email.SMTP, cfg.Notify.Email.From
	to, username := cfg.Notify.Email.To, cfg.Notify.Email.Username
	password := cfg.Notify.Email.Password

	// With no server set yet, offer a local relay if one is listening: it needs
	// no credentials (username stays empty → sendEmail skips auth).
	if smtp == "" {
		if local := notify.DetectLocalSMTP(ctx); local != "" {
			smtp = local
			ui.Info("detected a local mail server on " + local + " — leave username empty to send without authentication")
		}
	}

	if err := ui.Form([]ui.Field{
		{Title: "SMTP server (host:port)", Desc: "host:port, e.g. smtp.example.com:587.", Value: &smtp},
		{Title: "From address", Desc: "Envelope sender of the notification mails.", Value: &from},
		{Title: "To address", Desc: "Recipient of deploy results.", Value: &to},
		{Title: "SMTP username (empty disables authentication)", Desc: "Empty disables authentication — fine for a localhost relay.", Value: &username},
		{Title: "SMTP password", Desc: "Alt+R reveals or masks the stored value.", Secret: true, Value: &password},
	}); err != nil {
		return err
	}
	for _, field := range []struct{ key, value string }{
		{"notify.email.smtp", strings.TrimSpace(smtp)},
		{"notify.email.from", strings.TrimSpace(from)},
		{"notify.email.to", strings.TrimSpace(to)},
	} {
		if err := validateConfigValue(field.key, field.value); err != nil {
			return err
		}
	}

	cfg.Notify.Email.SMTP = strings.TrimSpace(smtp)
	cfg.Notify.Email.From = strings.TrimSpace(from)
	cfg.Notify.Email.To = strings.TrimSpace(to)
	cfg.Notify.Email.Username = strings.TrimSpace(username)
	cfg.Notify.Email.Password = strings.TrimSpace(password)

	if err := save(); err != nil {
		return err
	}
	if emailPartial(cfg.Notify.Email) {
		ui.Warn("email stays disabled until smtp, from and to are all set")
	}

	return nil
}

// telegramPartial reports a half-configured telegram channel — exactly one of
// the two required fields set. Configured() stays the authority on "can send";
// this exists only so the form warns instead of going silent.
func telegramPartial(t config.TelegramConfig) bool {
	return (t.Token != "") != (t.ChatID != "")
}

// emailPartial reports an email channel with some but not all of the fields
// Configured() requires. Username is optional and does not count.
func emailPartial(e config.EmailConfig) bool {
	return !e.Configured() && (e.SMTP != "" || e.From != "" || e.To != "")
}

// save persists the in-memory config and reports where it landed.
func save() error {
	if err := config.Save(flagConfig, Config()); err != nil {
		return err
	}

	path, err := configPath()
	if err != nil {
		return err
	}
	ui.Success("saved " + path)

	return nil
}

// configPath returns the config file in use: --config when given, the default
// location otherwise.
func configPath() (string, error) {
	if flagConfig != "" {
		return flagConfig, nil
	}

	return config.File()
}

// splitList parses a comma-separated flag or form value into a trimmed list,
// dropping empty entries so a trailing comma is harmless.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}

	return out
}

// newConfigPathCmd builds `config path`.
func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := configPath()
			if err != nil {
				return err
			}
			ui.Out(p)

			return nil
		},
	}
}

// newConfigGetCmd builds `config get <key>`.
func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Print a config value (secrets redacted)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			value, secret, err := configGet(Config(), args[0])
			if err != nil {
				return err
			}
			if secret {
				value = redact(value)
			}
			ui.Out(value)

			return nil
		},
	}
}

// newConfigSetCmd builds `config set <key> <value>`.
func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value and save",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := Config()
			if err := validateConfigValue(args[0], args[1]); err != nil {
				return err
			}
			if err := configSet(cfg, args[0], args[1]); err != nil {
				return err
			}
			if err := config.Save(flagConfig, cfg); err != nil {
				return err
			}
			if flagJSON {
				return ui.PrintJSON(map[string]string{"set": args[0]})
			}
			ui.Success("set " + args[0])

			return nil
		},
	}
}

// printConfig prints the effective config (secrets redacted) for non-TTY use.
func printConfig() error {
	cfg := Config()

	if flagJSON {
		return ui.PrintJSON(map[string]any{
			"listen":     cfg.Listen,
			"public_url": cfg.PublicURL,
			"github":     map[string]string{"token": tokenState(cfg.GitHub.Token)},
			"discovery":  map[string]any{"roots": cfg.Discovery.Roots, "prune": cfg.Discovery.Prune},
			"notify": map[string]any{
				"telegram": map[string]string{
					"token":   tokenState(cfg.Notify.Telegram.Token),
					"chat_id": cfg.Notify.Telegram.ChatID,
				},
				"email": map[string]string{
					"smtp":     cfg.Notify.Email.SMTP,
					"from":     cfg.Notify.Email.From,
					"to":       cfg.Notify.Email.To,
					"username": cfg.Notify.Email.Username,
					"password": tokenState(cfg.Notify.Email.Password),
				},
			},
		})
	}

	ui.Title("rec-deploy config")
	ui.KeyValue("listen", cfg.Listen)
	ui.KeyValue("public_url", orNotSet(cfg.PublicURL))
	ui.KeyValue("github", redact(cfg.GitHub.Token))
	ui.KeyList("roots", cfg.Discovery.Roots)
	ui.KeyList("prune", cfg.Discovery.Prune)
	ui.KeyValue("telegram", telegramSummary())
	ui.KeyValue("email", emailSummary())
	ui.Info("run in a terminal for the interactive form, or use `rec-deploy config set <key> <value>`")

	return nil
}

// telegramSummary describes the current Telegram configuration.
func telegramSummary() string {
	t := Config().Notify.Telegram
	if !t.Configured() {
		return "(not set)"
	}

	return "(chat " + t.ChatID + ", token " + redact(t.Token) + ")"
}

// emailSummary describes the current email configuration.
func emailSummary() string {
	e := Config().Notify.Email
	if !e.Configured() {
		return "(not set)"
	}

	return "(" + e.To + " via " + e.SMTP + ")"
}

// configGet returns a config value and whether it is secret.
func configGet(cfg *config.Config, key string) (value string, secret bool, err error) {
	field, ok := findConfigField(key)
	if !ok {
		return "", false, unknownKey(key)
	}
	return field.Get(cfg), field.Secret, nil
}

// configSet applies a value to a config key. List-valued keys take a
// comma-separated value.
func configSet(cfg *config.Config, key, value string) error {
	field, ok := findConfigField(key)
	if !ok {
		return unknownKey(key)
	}
	field.Set(cfg, value)
	return nil
}

// configKeys lists every key config get/set accepts, in the order they are
// shown to the user.
func configKeys() []string {
	keys := make([]string, len(configFields))
	for i, field := range configFields {
		keys[i] = field.Key
	}
	return keys
}

// unknownKey reports an unusable config key, listing the ones that do work.
func unknownKey(key string) error {
	return fmt.Errorf("unknown config key %q — known keys: %s", key, strings.Join(configKeys(), ", "))
}

// orNotSet renders an empty value as an explicit "(not set)" so a blank line is
// never mistaken for a configured empty string.
func orNotSet(s string) string {
	if s == "" {
		return "(not set)"
	}

	return s
}

// redact masks a secret to its last 4 characters. It is the only way a secret
// ever reaches human output.
func redact(s string) string {
	if s == "" {
		return "(not set)"
	}
	if len(s) <= 4 {
		return "••••"
	}

	return "••••" + s[len(s)-4:]
}

// tokenState reports "set"/"unset" for machine output, so --json never carries
// even the tail of a secret.
func tokenState(s string) string {
	if s == "" {
		return "unset"
	}

	return "set"
}
