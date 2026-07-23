package notify

import (
	"context"
	"html/template"
	"log/slog"
	"os"
	"strings"

	"github.com/rdcstarr/rec-deploy/internal/config"
)

// UpdateOutcome is what a self-update did to this server.
type UpdateOutcome int

const (
	// Updated means the new binary is installed and the daemon restarted onto it.
	Updated UpdateOutcome = iota
	// Installed means the binary is installed, but the daemon was already stopped
	// before the update and was left that way.
	Installed
	// RolledBack means the update failed and the previous binary was restored.
	RolledBack
)

// UpdateSummary is one self-update event, ready to render. It is the self-update
// counterpart of Summary: the update flow has an operator to reach and no deploy
// to describe, so it carries the version transition and the unit it touched
// rather than a repository and its installations.
type UpdateSummary struct {
	// From and To are the version before and after, e.g. "v1.1.0" and "v1.1.1".
	From string
	To   string
	// Unit is the systemd unit the update restarts, e.g. "rec-deploy.service".
	// It is unset for RolledBack, whose message does not name a unit.
	Unit    string
	Outcome UpdateOutcome
	// Detail carries the rollback failure text for RolledBack; unused otherwise.
	Detail string
}

// SendUpdate delivers a self-update event to journald, Telegram and email. Like
// Send it is best-effort: a channel failure is logged, never returned — a
// notification that cannot be delivered must not turn a completed update into a
// failure. Telegram gets plain text; email gets the self-update card with the
// plain text as its fallback body.
func SendUpdate(ctx context.Context, cfg config.NotifyConfig, u UpdateSummary) {
	host := hostname()
	subject := updateSubject(u, host)
	body := renderUpdatePlain(u, host)

	slog.Info(subject)

	if cfg.Telegram.Configured() {
		if err := sendTelegram(ctx, cfg.Telegram, RenderUpdateTelegramHTML(u, host), body); err != nil {
			slog.Error("telegram notification failed", "error", err)
		}
	}

	if cfg.Email.Configured() {
		html, err := RenderUpdateHTML(u, host)
		if err != nil {
			slog.Error("html rendering failed — sending plain text", "error", err)
			html = ""
		}
		if err := sendEmail(ctx, cfg.Email, subject, body, html); err != nil {
			slog.Error("email notification failed", "error", err)
		}
	}
}

// updateSubject is the one-line headline, naming the server so an operator who
// runs rec-deploy on many boxes can tell which one updated.
func updateSubject(u UpdateSummary, host string) string {
	var s string
	switch u.Outcome {
	case Installed:
		s = "rec-deploy: installed " + u.To
	case RolledBack:
		s = "rec-deploy: rolled back " + u.To
	default:
		s = "rec-deploy: updated to " + u.To
	}
	if host != "" {
		s += " on " + host
	}

	return s
}

// renderUpdatePlain builds the plain-text body Telegram sends and email falls
// back to. It names the server in the body — Telegram delivers only the body, so
// without this the operator could not tell which box a message came from.
func renderUpdatePlain(u UpdateSummary, host string) string {
	var body string
	switch u.Outcome {
	case Installed:
		body = "rec-deploy " + u.From + " → " + u.To + " is installed, but " + u.Unit + " was not running before the update and was left stopped.\n" +
			"start it when ready: systemctl start " + u.Unit + "\n"
	case RolledBack:
		where := "this server"
		if host != "" {
			where = host
		}
		body = "updating rec-deploy " + u.From + " → " + u.To + " failed on " + where + ".\n"
		if u.Detail != "" {
			body += "\n" + u.Detail + "\n"
		}
	default:
		body = "rec-deploy " + u.From + " → " + u.To + "; " + u.Unit + " restarted.\n"
	}

	// RolledBack already names the server in its sentence above.
	if host != "" && u.Outcome != RolledBack {
		body += "host: " + host + "\n"
	}

	return body
}

// updateEmailTemplate is the self-update card: the same terminal-window chrome as
// the deploy card (emailTemplate), with a self-update body — one command line and
// one verdict — instead of a push and its installations. Table layout and inline
// styles only, for the same cross-client reasons as the deploy card.
const updateEmailTemplate = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="color-scheme" content="dark"><title>{{.Subject}}</title></head>
<body style="margin:0;padding:0;background:#1a1d21">
<table width="100%" cellpadding="0" cellspacing="0" style="background:#1a1d21"><tr><td align="center" style="padding:28px 12px">
<table width="600" cellpadding="0" cellspacing="0" style="max-width:600px;width:100%;background:#0e1114;border:1px solid {{.Border}};border-radius:10px;border-collapse:separate;overflow:hidden">
<tr><td style="padding:11px 16px;background:{{.ChromeBG}};border-bottom:1px solid {{.Border}}">
<span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:#d05a4e"></span>
<span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:#e0a63f;margin-left:5px"></span>
<span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:#39a862;margin-left:5px"></span>
<span style="font:11px ui-monospace,'Cascadia Mono',Consolas,'Liberation Mono',monospace;color:#6d7278;padding-left:12px">rec-deploy — self-update{{with .Host}} · {{.}}{{end}}</span>
</td></tr>
<tr><td style="padding:24px 26px 26px;font:13.5px/1.85 ui-monospace,'Cascadia Mono',Consolas,'Liberation Mono',monospace;color:#c9d1d9">
<span style="color:#6d7278">$ self-update {{.From}} → {{.To}}</span><br>
{{with .Host}}<span style="color:#58a6ff">›</span> host <span style="color:#c9d1d9">{{.}}</span><br>
{{end}}
<br>
<span style="color:{{.VerdictColor}};font-weight:bold">{{.VerdictGlyph}} {{.VerdictWord}}</span> <span style="color:#6d7278">— {{.Tail}}</span><br>
<span style="color:#3d434a">— rec-deploy {{.Version}}{{with .Host}} · {{.}}{{end}}</span>
</td></tr>
</table>
</td></tr></table>
</body></html>`

var updateEmailTmpl = template.Must(template.New("update-email").Parse(updateEmailTemplate))

// updateView is updateEmailTemplate's data: the verdict glyph, colour and word
// are picked in Go so the template stays free of status logic.
type updateView struct {
	Subject      string
	From         string
	To           string
	Tail         string
	Version      string
	Host         string
	VerdictGlyph string
	VerdictColor string
	VerdictWord  string
	Border       string
	ChromeBG     string
}

// RenderUpdateHTML builds the HTML body the email channel sends for a self-update
// — the version transition rendered as the same terminal card as a deploy. The
// rollback failure text passes through html/template's contextual escaping; it is
// an error string, never trusted markup.
func RenderUpdateHTML(u UpdateSummary, host string) (string, error) {
	v := updateView{
		Subject:  updateSubject(u, host),
		From:     u.From,
		To:       u.To,
		Version:  u.To,
		Host:     host,
		Border:   "#23272c",
		ChromeBG: "#161a1e",
	}
	switch u.Outcome {
	case Installed:
		v.VerdictGlyph, v.VerdictColor, v.VerdictWord = "›", "#e0a63f", "installed"
		v.Tail = u.Unit + " was left stopped — start it: systemctl start " + u.Unit
	case RolledBack:
		v.VerdictGlyph, v.VerdictColor, v.VerdictWord = "!", "#f2635a", "rolled back"
		v.Tail = u.Detail
		v.Version = u.From
		v.Border, v.ChromeBG = "#3a2724", "#1a1416"
	default:
		v.VerdictGlyph, v.VerdictColor, v.VerdictWord = "✓", "#39d47f", "updated"
		v.Tail = u.Unit + " restarted"
	}

	var b strings.Builder
	if err := updateEmailTmpl.Execute(&b, v); err != nil {
		return "", err
	}

	return b.String(), nil
}

// RenderUpdateTelegramHTML builds the compact self-update card used by
// Telegram. Dynamic values are escaped before entering Telegram's HTML mode.
func RenderUpdateTelegramHTML(u UpdateSummary, host string) string {
	glyph, title, detail := "✅", "rec-deploy updated", u.Unit+" restarted"
	switch u.Outcome {
	case Installed:
		glyph, title = "⚠️", "rec-deploy installed"
		detail = u.Unit + " remains stopped"
	case RolledBack:
		glyph, title = "❌", "rec-deploy update rolled back"
		detail = u.Detail
		if detail == "" {
			detail = "the previous version was restored"
		}
	}

	var b strings.Builder
	b.WriteString("<b>" + glyph + " " + title + "</b>\n")
	if host != "" {
		b.WriteString("<code>" + esc(host) + "</code>\n")
	}
	b.WriteString("\n<code>" + esc(u.From) + " → " + esc(u.To) + "</code>\n")
	b.WriteString("▸ " + esc(detail))
	if u.Outcome == Installed && u.Unit != "" {
		b.WriteString("\n\n<code>systemctl start " + esc(u.Unit) + "</code>")
	}

	return b.String()
}

// hostname returns this server's host name, or "" when it cannot be read. Every
// notification names the server it ran on, so a message from a fleet is
// attributable to one box.
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}

	return h
}
