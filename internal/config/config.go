package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config holds rec-deploy's settings, read from /etc/rec-deploy/config.yaml (root) or
// ~/.config/rec-deploy/config.yaml, with REC_DEPLOY_* environment overrides.
type Config struct {
	// Listen is the address the webhook daemon binds, e.g. 0.0.0.0:9000.
	Listen string `mapstructure:"listen"`
	// PublicURL is the origin GitHub posts to; it is what `repo add` registers
	// as the webhook URL, e.g. http://1.2.3.4:9000.
	PublicURL string `mapstructure:"public_url"`
	// GitHub holds the API credentials.
	GitHub GitHubConfig `mapstructure:"github"`
	// Discovery configures the filesystem walk that finds deploy targets.
	Discovery DiscoveryConfig `mapstructure:"discovery"`
	// Notify configures where deploy summaries are sent.
	Notify NotifyConfig `mapstructure:"notify"`
	// Initialized records that the init wizard ran every step without error. It
	// is what tells the interactive hub to stop offering setup on a server that
	// has already been set up.
	Initialized bool `mapstructure:"initialized"`
}

// GitHubConfig holds the GitHub API credentials.
type GitHubConfig struct {
	// Token is a personal access token with the repo and admin:repo_hook
	// scopes (sensitive).
	Token string `mapstructure:"token"`
}

// DiscoveryConfig configures the filesystem walk that finds deploy targets.
type DiscoveryConfig struct {
	// Roots are glob patterns the scan starts from, e.g. /home/*/web/*/public_html.
	Roots []string `mapstructure:"roots"`
	// Prune are directory names the walk never descends into.
	Prune []string `mapstructure:"prune"`
}

// NotifyConfig configures deploy notifications. journald is always on and needs
// no configuration.
type NotifyConfig struct {
	// Telegram configures Telegram Bot API notifications.
	Telegram TelegramConfig `mapstructure:"telegram"`
	// Email configures SMTP notifications.
	Email EmailConfig `mapstructure:"email"`
}

// TelegramConfig holds Telegram Bot API notification settings.
type TelegramConfig struct {
	// Token is the bot token from @BotFather (sensitive).
	Token string `mapstructure:"token"`
	// ChatID is the chat or user notifications are sent to. It is a string
	// because Telegram IDs can be negative (groups) or "@channelusername".
	ChatID string `mapstructure:"chat_id"`
}

// Configured reports whether Telegram notifications can be sent.
func (t TelegramConfig) Configured() bool { return t.Token != "" && t.ChatID != "" }

// EmailConfig holds SMTP notification settings.
type EmailConfig struct {
	// SMTP is the server as host:port, e.g. smtp.example.com:587.
	SMTP string `mapstructure:"smtp"`
	// From is the envelope sender.
	From string `mapstructure:"from"`
	// To is the recipient.
	To string `mapstructure:"to"`
	// Username is the SMTP auth user; empty disables authentication.
	Username string `mapstructure:"username"`
	// Password is the SMTP auth password (sensitive).
	Password string `mapstructure:"password"`
}

// Configured reports whether email notifications can be sent.
func (e EmailConfig) Configured() bool { return e.SMTP != "" && e.From != "" && e.To != "" }

// Load reads configuration from the given file (or the default location when
// empty), layering REC_DEPLOY_* environment overrides on top of the defaults. A
// missing config file is not an error; a malformed one is.
func Load(file string) (*Config, error) {
	v := viper.New()

	v.SetDefault("listen", "0.0.0.0:9000")
	v.SetDefault("discovery.roots", []string{"/home/*/web/*/public_html", "/var/www"})
	v.SetDefault("discovery.prune", []string{"node_modules", "vendor", "uploads", "cache"})

	v.SetEnvPrefix("REC_DEPLOY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if file != "" {
		v.SetConfigFile(file)
	} else {
		dir, err := Dir()
		if err != nil {
			return nil, err
		}

		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(dir)
	}

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		// A missing file (the default search, or an explicit --config that does
		// not exist yet) is fine — defaults apply and a later Save creates it.
		// Anything else, a malformed YAML above all, is a hard error.
		if !errors.As(err, &notFound) && !os.IsNotExist(err) {
			return nil, err
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Save writes the managed keys back to the config file (the default location
// when path is empty), preserving keys it does not manage, and restricts it to
// 0600 in a 0700 directory — it holds the GitHub token.
func Save(path string, cfg *Config) error {
	if path == "" {
		f, err := File()
		if err != nil {
			return err
		}
		path = f
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	// Load existing content so sibling keys survive; absence is fine. Viper cannot
	// unset a key, so it is read into a throwaway instance and the writer is
	// seeded without the mcp section a former release left behind — that section
	// can hold a Cloudflare API token, which must not be carried forward. Every
	// other key survives, known or not, so an older binary rolled back to keeps
	// what a newer one saved.
	existing := viper.New()
	existing.SetConfigFile(path)
	if err := existing.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && !os.IsNotExist(err) {
			return err
		}
	}
	settings := existing.AllSettings()
	delete(settings, "mcp")

	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigPermissions(0o600) // create the file owner-only — it holds secrets
	if err := v.MergeConfigMap(settings); err != nil {
		return err
	}

	v.Set("listen", cfg.Listen)
	v.Set("public_url", cfg.PublicURL)
	v.Set("github.token", cfg.GitHub.Token)
	v.Set("discovery.roots", cfg.Discovery.Roots)
	v.Set("discovery.prune", cfg.Discovery.Prune)
	v.Set("notify.telegram.token", cfg.Notify.Telegram.Token)
	v.Set("notify.telegram.chat_id", cfg.Notify.Telegram.ChatID)
	v.Set("notify.email.smtp", cfg.Notify.Email.SMTP)
	v.Set("notify.email.from", cfg.Notify.Email.From)
	v.Set("notify.email.to", cfg.Notify.Email.To)
	v.Set("notify.email.username", cfg.Notify.Email.Username)
	v.Set("notify.email.password", cfg.Notify.Email.Password)
	v.Set("initialized", cfg.Initialized)

	if err := v.WriteConfigAs(path); err != nil {
		return err
	}

	return os.Chmod(path, 0o600)
}
