// Package notify sends one summary per deploy to journald, Telegram and email.
//
// It is best-effort by design: a Telegram outage must not fail a deploy that
// already succeeded. Every channel failure is logged, none is fatal.
//
// The package is a leaf — it never imports internal/deploy. The caller maps a
// deploy result into a Summary, which keeps this testable without an engine.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rdcstarr/rec-deploy/internal/config"
)

// Summary is one deploy, fanned out over every installation, ready to render.
type Summary struct {
	Repository string
	Ref        string
	SHA        string
	Message    string
	Author     string
	// Status is the deploy's overall outcome: success, failed or skipped.
	Status string
	// Error is set when the deploy failed before reaching any path — a zero
	// installation count, for instance, which an old implementation reports as silence.
	Error string
	Paths []PathSummary
}

// PathSummary is one installation's outcome.
type PathSummary struct {
	Path      string
	User      string
	Status    string
	Reason    string
	RanAsRoot bool
}

// Subject is the one-line headline: repository, branch, outcome.
func Subject(s Summary) string {
	branch := strings.TrimPrefix(s.Ref, "refs/heads/")

	return fmt.Sprintf("rec-deploy: %s@%s %s", s.Repository, branch, s.Status)
}

// Render builds the plain-text body every channel sends.
func Render(s Summary) string {
	var b strings.Builder

	b.WriteString(Subject(s) + "\n")

	if s.SHA != "" {
		b.WriteString("commit: " + short(s.SHA))
		if s.Author != "" {
			b.WriteString(" by " + s.Author)
		}
		b.WriteString("\n")
	}
	if s.Message != "" {
		b.WriteString("message: " + firstLine(s.Message) + "\n")
	}
	if s.Error != "" {
		b.WriteString("error: " + s.Error + "\n")
	}

	for _, p := range s.Paths {
		b.WriteString("\n" + p.Status + "  " + p.Path)
		if p.RanAsRoot {
			// Push access to this repository is root on this server.
			b.WriteString("  ⚠ root")
		} else if p.User != "" {
			b.WriteString("  (" + p.User + ")")
		}
		if p.Reason != "" {
			b.WriteString("\n        " + p.Reason)
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ChannelResult is one channel's outcome for one notification. Deliver
// returns one per channel — nothing about a delivery is silent.
type ChannelResult struct {
	// Channel names the channel: "telegram" or "email".
	Channel string `json:"channel"`
	// Skipped reports the channel was not configured — no send was
	// attempted.
	Skipped bool `json:"skipped"`
	// Detail explains Skipped (which fields are missing) or carries the
	// send error's text.
	Detail string `json:"detail,omitempty"`
	// Err is the underlying send error, nil when sent or skipped. Detail
	// already carries its text for JSON output.
	Err error `json:"-"`
}

// Deliver renders and sends s to every notification channel and reports each
// outcome — nothing is silent here. Send wraps it for the deploy path, where
// failures are logged; `notify test` prints it, which is how an operator sees
// an SMTP error without journalctl.
func Deliver(ctx context.Context, cfg config.NotifyConfig, s Summary) []ChannelResult {
	results := make([]ChannelResult, 0, 2)
	body := Render(s)

	if !cfg.Telegram.Configured() {
		results = append(results, ChannelResult{Channel: "telegram", Skipped: true, Detail: missingTelegram(cfg.Telegram)})
	} else if err := sendTelegram(ctx, cfg.Telegram, RenderTelegramHTML(s), body); err != nil {
		results = append(results, ChannelResult{Channel: "telegram", Detail: err.Error(), Err: err})
	} else {
		results = append(results, ChannelResult{Channel: "telegram"})
	}

	if !cfg.Email.Configured() {
		results = append(results, ChannelResult{Channel: "email", Skipped: true, Detail: missingEmail(cfg.Email)})
	} else {
		html, err := RenderHTML(s)
		if err != nil {
			slog.Error("html rendering failed — sending plain text", "error", err)
			html = ""
		}
		if err := sendEmail(ctx, cfg.Email, Subject(s), body, html); err != nil {
			results = append(results, ChannelResult{Channel: "email", Detail: err.Error(), Err: err})
		} else {
			results = append(results, ChannelResult{Channel: "email"})
		}
	}

	return results
}

// Send delivers the summary to every configured channel: journald always,
// Telegram and email when configured. Failures are logged, not returned — the
// deploy has already happened, and a notification that cannot be delivered
// must not turn a success into a failure.
func Send(ctx context.Context, cfg config.NotifyConfig, s Summary) {
	toJournald(s, Render(s))

	for _, r := range Deliver(ctx, cfg, s) {
		switch {
		case r.Skipped:
			slog.Debug(r.Channel+" notification skipped", "detail", r.Detail)
		case r.Err != nil:
			slog.Error(r.Channel+" notification failed", "error", r.Err)
		}
	}
}

// missingTelegram names the unset Telegram fields for a skipped
// ChannelResult's Detail — "bot token is not set", "chat id is not set", or
// "bot token and chat id are not set" when both are empty.
func missingTelegram(cfg config.TelegramConfig) string {
	var missing []string
	if cfg.Token == "" {
		missing = append(missing, "bot token")
	}
	if cfg.ChatID == "" {
		missing = append(missing, "chat id")
	}

	return missingDetail(missing)
}

// missingEmail names the unset email fields for a skipped ChannelResult's
// Detail, e.g. "smtp is not set" or "smtp, from and to are not set".
func missingEmail(cfg config.EmailConfig) string {
	var missing []string
	if cfg.SMTP == "" {
		missing = append(missing, "smtp")
	}
	if cfg.From == "" {
		missing = append(missing, "from")
	}
	if cfg.To == "" {
		missing = append(missing, "to")
	}

	return missingDetail(missing)
}

// missingDetail joins field names into "X is not set" / "X and Y are not
// set" / "X, Y and Z are not set".
func missingDetail(missing []string) string {
	verb := "is not set"
	if len(missing) > 1 {
		verb = "are not set"
	}

	return joinNames(missing) + " " + verb
}

// joinNames joins names the way English lists them: "a", "a and b", or
// "a, b and c".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// short truncates a SHA to its first seven characters.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}

	return sha
}

// firstLine returns the first line of a commit message.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")

	return strings.TrimSpace(line)
}
