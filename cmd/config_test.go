package cmd

import (
	"regexp"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/ui"
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

// discoveryCountPattern matches the shape of the deleted discoverySummary —
// "(%d roots, %d pruned)" — so a count-based description regresses this test
// even though its exact numbers differ from any literal value fixed below.
var discoveryCountPattern = regexp.MustCompile(`(?i)\d+\s*roots?\b|\bpruned?\b`)

// TestConfigMenuDescribesInsteadOfDumping pins that the section list says what
// each section is for. It used to print the current values — "(chat 5775201531,
// token ••••oJZc)" — which is noise on a screen whose whole job is to be picked
// from, and which put a masked secret on a screen that did not need one. The
// discovery section used a different shape — "(2 roots, 2 pruned)", a count
// rather than a literal value — so it is fixtured and checked separately from
// the literal-value sections below.
func TestConfigMenuDescribesInsteadOfDumping(t *testing.T) {
	ui.SetColor(false)

	saved := cfg
	defer func() { cfg = saved }()
	cfg = &config.Config{
		Listen:    "0.0.0.0:9000",
		PublicURL: "http://198.51.100.7:9000",
		GitHub:    config.GitHubConfig{Token: "ghp_averysecrettokenvalue"},
		Discovery: config.DiscoveryConfig{
			Roots: []string{"/var/www", "/home/*/web/*/public_html"},
			Prune: []string{"vendor", "node_modules"},
		},
		Notify: config.NotifyConfig{
			Telegram: config.TelegramConfig{Token: "12345:secret", ChatID: "5775201531"},
			Email:    config.EmailConfig{SMTP: "smtp.example.com:587", From: "a@example.com", To: "b@example.com"},
		},
	}

	leaked := []string{
		"0.0.0.0:9000", "198.51.100.7", "5775201531", "smtp.example.com", "b@example.com", "••••",
		"/var/www", "/home/*/web/*/public_html", "vendor", "node_modules",
	}
	for _, option := range configMenuOptions() {
		for _, leak := range leaked {
			if strings.Contains(option.Label, leak) {
				t.Errorf("the config menu still shows the value %q: %q", leak, option.Label)
			}
		}
		if discoveryCountPattern.MatchString(option.Label) {
			t.Errorf("the config menu still shows a discovery count instead of a description: %q", option.Label)
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

// TestConfigMenuOptionsCarryDescriptions pins that each section's registry
// description actually reaches the rendered menu label, not merely that the
// registry has one filled in. TestEverySectionIsDescribed only checks the
// registry, and TestConfigMenuDescribesInsteadOfDumping only checks that bad
// values are absent — dropping Description from the DescribedOption literal
// in configMenuOptions would leave both green while silently removing every
// description this feature exists to show. Driven off configSections so a new
// section cannot be added without this check covering it too.
func TestConfigMenuOptionsCarryDescriptions(t *testing.T) {
	ui.SetColor(false)

	options := configMenuOptions()
	for _, section := range configSections {
		var label string
		var found bool
		for _, option := range options {
			if option.Value == section.Key {
				label, found = option.Label, true
				break
			}
		}
		if !found {
			t.Fatalf("no menu option for section %q", section.Key)
		}
		if !strings.Contains(label, section.Description) {
			t.Errorf("section %q label %q does not carry its description %q", section.Key, label, section.Description)
		}
	}
}

// TestNotificationSectionsOfferATest pins that notify leaves the hub with a
// home: sending a test message belongs beside the settings it exercises, and
// the notification sections are the only interactive way to it now. The
// negative half is driven off configSections rather than a hardcoded list of
// non-notification sections, so a change that leaked "test" into every
// section — including ones added later — cannot pass by omission.
func TestNotificationSectionsOfferATest(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()
	cfg = &config.Config{}

	notifySections := map[string]bool{"telegram": true, "email": true}

	for section := range notifySections {
		var found bool
		for _, option := range configSectionOptions(section) {
			if option.Value == "test" {
				found = true
			}
		}
		if !found {
			t.Errorf("the %s section does not offer a test send", section)
		}
	}

	for _, section := range configSections {
		if notifySections[section.Key] {
			continue
		}
		for _, option := range configSectionOptions(section.Key) {
			if option.Value == "test" {
				t.Errorf("the %s section offers a notification test", section.Key)
			}
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

// TestChannelFailureChoiceOffersAllThreeWaysOut pins the contract that keeps a
// rejected credential from being saved silently: the operator must be able to
// correct it, override the verdict, or walk away — and the picker's values are
// what configureTelegram and configureEmail branch on, so a renamed one would
// silently fall through to "save anyway".
func TestChannelFailureChoiceOffersAllThreeWaysOut(t *testing.T) {
	ui.SetColor(false)
	t.Cleanup(func() { ui.SetColor(true) })

	options := channelFailureOptions("Telegram", "re-enter the bot token and chat ID")

	got := make(map[string]bool, len(options))
	for _, o := range options {
		got[o.Value] = true
	}
	for _, want := range []string{"retry", "save", "skip"} {
		if !got[want] {
			t.Errorf("the failure screen offers no %q way out: %#v", want, options)
		}
	}
}

// TestValidateEmailFieldsRejectsWhatConfigSetRejects pins that the interactive
// form and the scripted setter agree: a value `config set` refuses must not slip
// through the wizard just because it was typed into a form instead.
func TestValidateEmailFieldsRejectsWhatConfigSetRejects(t *testing.T) {
	if err := validateEmailFields("smtp.example.com:587", "deploy@example.com", "ops@example.com"); err != nil {
		t.Errorf("a valid trio was rejected: %v", err)
	}
	if err := validateEmailFields("smtp.example.com", "deploy@example.com", "ops@example.com"); err == nil {
		t.Error("an SMTP server with no port was accepted")
	}
	if err := validateEmailFields("smtp.example.com:587", "not-an-address", "ops@example.com"); err == nil {
		t.Error("a malformed sender address was accepted")
	}
}
