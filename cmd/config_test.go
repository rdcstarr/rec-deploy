package cmd

import (
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/config"
)

// TestTelegramPartial pins the warn condition: exactly one of the two required
// fields set. Fully empty and fully configured must stay silent.
func TestTelegramPartial(t *testing.T) {
	if telegramPartial(config.TelegramConfig{}) {
		t.Error("empty config is not partial")
	}
	if !telegramPartial(config.TelegramConfig{Token: "t"}) {
		t.Error("token without chat id is partial")
	}
	if !telegramPartial(config.TelegramConfig{ChatID: "42"}) {
		t.Error("chat id without token is partial")
	}
	if telegramPartial(config.TelegramConfig{Token: "t", ChatID: "42"}) {
		t.Error("fully configured is not partial")
	}
}

// TestEmailPartial pins the warn condition: some but not all of the fields
// Configured() requires. Username alone does not make the channel partial —
// it is optional.
func TestEmailPartial(t *testing.T) {
	if emailPartial(config.EmailConfig{}) {
		t.Error("empty config is not partial")
	}
	if !emailPartial(config.EmailConfig{To: "ops@example.com"}) {
		t.Error("to without smtp/from is partial")
	}
	if !emailPartial(config.EmailConfig{SMTP: "smtp.example.com:587", From: "a@b"}) {
		t.Error("smtp+from without to is partial")
	}
	if emailPartial(config.EmailConfig{SMTP: "smtp.example.com:587", From: "a@b", To: "c@d"}) {
		t.Error("fully configured is not partial")
	}
	if emailPartial(config.EmailConfig{Username: "u"}) {
		t.Error("username alone is optional, not partial")
	}
}

func TestSecretSectionOptionsStayMasked(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()

	cfg = &config.Config{}
	cfg.GitHub.Token = "ghp_secrettoken"
	cfg.Notify.Telegram.Token = "tg_secret"
	cfg.Notify.Email.Password = "smtp_secret"

	for _, section := range []string{"github", "telegram", "email"} {
		for _, option := range configSectionOptions(section) {
			for _, secret := range []string{"ghp_secrettoken", "tg_secret", "smtp_secret"} {
				if strings.Contains(option.Label, secret) {
					t.Errorf("%s overview exposes secret in %q", section, option.Label)
				}
			}
		}
	}
}

func TestConfigRegistryDrivesGetSetAndCopy(t *testing.T) {
	for _, field := range configFields {
		cfg := &config.Config{}
		value := "value"
		if field.Key == "discovery.roots" || field.Key == "discovery.prune" {
			value = "one,two"
		}
		if err := configSet(cfg, field.Key, value); err != nil {
			t.Fatalf("configSet(%q): %v", field.Key, err)
		}
		got, secret, err := configGet(cfg, field.Key)
		if err != nil {
			t.Fatalf("configGet(%q): %v", field.Key, err)
		}
		if got != value || secret != field.Secret {
			t.Errorf("field %q round trip = %q secret=%v", field.Key, got, secret)
		}
		title, desc := configFieldCopy(field.Key)
		if title != field.Title || desc != field.Description {
			t.Errorf("field %q copy drifted from registry", field.Key)
		}
	}
	if len(configKeys()) != len(configFields) {
		t.Fatalf("config keys and registry differ: %d != %d", len(configKeys()), len(configFields))
	}
}

// TestConfigMenuDescribesInsteadOfDumping pins that the section list says what
// each section is for. It used to print the current values — "(chat 5775201531,
// token ••••oJZc)" — which is noise on a screen whose whole job is to be picked
// from, and which put a masked secret on a screen that did not need one.
func TestConfigMenuDescribesInsteadOfDumping(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()
	cfg = &config.Config{
		Listen:    "0.0.0.0:9000",
		PublicURL: "http://198.51.100.7:9000",
		GitHub:    config.GitHubConfig{Token: "ghp_averysecrettokenvalue"},
		Notify: config.NotifyConfig{
			Telegram: config.TelegramConfig{Token: "12345:secret", ChatID: "5775201531"},
			Email:    config.EmailConfig{SMTP: "smtp.example.com:587", From: "a@example.com", To: "b@example.com"},
		},
	}

	for _, option := range configMenuOptions() {
		for _, leaked := range []string{"0.0.0.0:9000", "198.51.100.7", "5775201531", "smtp.example.com", "b@example.com", "••••"} {
			if strings.Contains(option.Label, leaked) {
				t.Errorf("the config menu still shows the value %q: %q", leaked, option.Label)
			}
		}
	}

	if len(configMenuOptions()) != len(configSections)+1 {
		t.Errorf("the config menu is not one entry per section plus Exit: %d sections, %d options", len(configSections), len(configMenuOptions()))
	}
}

// TestEverySectionIsDescribed pins that adding a section to the registry means
// writing what it is for, not shipping a blank row.
func TestEverySectionIsDescribed(t *testing.T) {
	for _, section := range configSections {
		if section.Description == "" {
			t.Errorf("config section %q has no description", section.Key)
		}
	}
}

func TestValidateConfigValue(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr bool
	}{
		{name: "listen", key: "listen", value: "0.0.0.0:9000"},
		{name: "listen missing port", key: "listen", value: "localhost", wantErr: true},
		{name: "listen bad port", key: "listen", value: "localhost:70000", wantErr: true},
		{name: "public https", key: "public_url", value: "https://deploy.example.com"},
		{name: "public relative", key: "public_url", value: "/hooks", wantErr: true},
		{name: "smtp empty disables", key: "notify.email.smtp", value: ""},
		{name: "smtp endpoint", key: "notify.email.smtp", value: "smtp.example.com:587"},
		{name: "email", key: "notify.email.to", value: "ops@example.com"},
		{name: "email display name", key: "notify.email.to", value: "Ops <ops@example.com>", wantErr: true},
		{name: "root glob", key: "discovery.roots", value: "/home/*/web,/var/www"},
		{name: "bad root glob", key: "discovery.roots", value: "[", wantErr: true},
		{name: "prune names", key: "discovery.prune", value: "vendor,node_modules"},
		{name: "prune path", key: "discovery.prune", value: "cache/files", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfigValue(tt.key, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateConfigValue(%q, %q) error = %v", tt.key, tt.value, err)
			}
		})
	}
}
