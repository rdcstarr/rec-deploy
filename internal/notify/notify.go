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
	// Pipeline names what ran: "setup" or "post_deploy". An install that ran
	// unattended across a fleet must not look like a routine push.
	Pipeline string
	Paths    []PathSummary
}

// PathSummary is one installation's outcome.
type PathSummary struct {
	Path      string
	User      string
	Status    string
	Reason    string
	RanAsRoot bool
}

// Subject is the one-line headline: repository, branch, outcome. A setup run
// that targeted one branch of a fleet (`repo setup <repo> --branch <b>`) says
// so in the "rec-deploy" prefix, since branchLabel's slot is busy with the
// real branch there — see needsSetupNote.
func Subject(s Summary) string {
	prefix := "rec-deploy"
	if needsSetupNote(s) {
		prefix += " setup"
	}

	return fmt.Sprintf("%s: %s@%s %s", prefix, s.Repository, branchLabel(s), s.Status)
}

// branchLabel is what every renderer shows where the pushed branch would
// sit: the branch name for an ordinary deploy, or "setup" when there is none
// to show — a first install, `repo deploy --setup`, and a repository_dispatch
// sent to no particular branch are all refless, and that slot is exactly
// where a routine push would name its branch. Left blank, a setup run that
// went out unattended across a fleet reads identically to an ordinary push;
// filled with "setup", it reads as what it is at a glance, in the same spot
// every other run already uses to say so.
func branchLabel(s Summary) string {
	if branch := strings.TrimPrefix(s.Ref, "refs/heads/"); branch != "" {
		return branch
	}
	if s.Pipeline == "setup" {
		return "setup"
	}

	return ""
}

// needsSetupNote reports whether a setup run needs its own "setup" marker
// outside the branch slot. branchLabel already answers for a refless setup
// run — repo@setup, in the exact spot a routine push would name its branch —
// but `repo setup <repo> --branch <b>` sends a repository_dispatch that
// targets one branch of a fleet, and that branch is real information the
// slot must keep. A setup run has to read as one whatever its ref, so when
// there is a real branch to show, every renderer marks the run's own
// title/verdict word instead of crowding the branch it must not replace.
func needsSetupNote(s Summary) bool {
	return s.Pipeline == "setup" && strings.TrimPrefix(s.Ref, "refs/heads/") != ""
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
	} else if s.Author != "" {
		// No commit to hang the author off: a repository_dispatch carries none by
		// design, and its author is the GitHub login of whoever sent it. That is
		// the only record of who asked for a setup that ran unattended on every
		// server registered on the repository, so it gets its own line rather than
		// being dropped with the commit it does not have.
		b.WriteString("requested by " + s.Author + "\n")
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
// failures are logged; `notifications test` prints it, which is how an operator
// sees an SMTP error without journalctl.
func Deliver(ctx context.Context, cfg config.NotifyConfig, s Summary) []ChannelResult {
	return []ChannelResult{DeliverTelegram(ctx, cfg.Telegram, s), DeliverEmail(ctx, cfg.Email, s)}
}

// DeliverTelegram sends s to Telegram and reports the outcome. It is exported
// separately from Deliver so a test triggered from the Telegram screen reaches
// Telegram and nothing else — a probe sent from one channel's settings that
// also mails somebody is a surprise, not a test.
func DeliverTelegram(ctx context.Context, cfg config.TelegramConfig, s Summary) ChannelResult {
	if !cfg.Configured() {
		return ChannelResult{Channel: "telegram", Skipped: true, Detail: MissingTelegram(cfg)}
	}
	if err := sendTelegram(ctx, cfg, RenderTelegramHTML(s), Render(s)); err != nil {
		return ChannelResult{Channel: "telegram", Detail: err.Error(), Err: err}
	}

	return ChannelResult{Channel: "telegram"}
}

// DeliverEmail sends s by email and reports the outcome. See DeliverTelegram
// for why the channels are reachable one at a time.
func DeliverEmail(ctx context.Context, cfg config.EmailConfig, s Summary) ChannelResult {
	if !cfg.Configured() {
		return ChannelResult{Channel: "email", Skipped: true, Detail: MissingEmail(cfg)}
	}

	html, err := RenderHTML(s)
	if err != nil {
		slog.Error("html rendering failed — sending plain text", "error", err)
		html = ""
	}
	if err := sendEmail(ctx, cfg, Subject(s), Render(s), html); err != nil {
		return ChannelResult{Channel: "email", Detail: err.Error(), Err: err}
	}

	return ChannelResult{Channel: "email"}
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

// MissingTelegram names the unset Telegram fields for a skipped
// ChannelResult's Detail — "bot token is not set", "chat id is not set", or
// "bot token and chat id are not set" when both are empty.
func MissingTelegram(cfg config.TelegramConfig) string {
	var missing []string
	if cfg.Token == "" {
		missing = append(missing, "bot token")
	}
	if cfg.ChatID == "" {
		missing = append(missing, "chat id")
	}

	return missingDetail(missing)
}

// MissingEmail names the unset email fields for a skipped ChannelResult's
// Detail, e.g. "smtp is not set" or "smtp, from and to are not set".
func MissingEmail(cfg config.EmailConfig) string {
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
