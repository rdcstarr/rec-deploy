package notify

import (
	"html"
	"strings"

	"github.com/rdcstarr/rec-deploy/internal/buildinfo"
)

// esc escapes the three characters Telegram's HTML parse mode reserves. Every
// push-controlled value goes through it — same boundary as the email card's
// autoescaping, made explicit because this file concatenates.
func esc(s string) string { return html.EscapeString(s) }

// telegramVerdict maps a deploy's overall status to the card header's glyph
// and word — the same three-state mapping as the email card (success /
// failed / neutral-raw-status), translated to Telegram's tag set.
func telegramVerdict(status string) (glyph, word string) {
	switch status {
	case "success":
		return "✅", "deployed"
	case "failed":
		return "❌", "failed"
	case "test":
		return "🧪", "notification test"
	default: // "skipped" and anything neutral
		return "▸", status
	}
}

// telegramPathGlyph maps one installation's status to its line glyph. Failed
// and rolled-back installations share the failure glyph, matching the email
// card's grouping.
func telegramPathGlyph(status string) string {
	switch status {
	case "success":
		return "✅"
	case "failed", "rolled_back":
		return "❌"
	default:
		return "▸"
	}
}

// RenderTelegramHTML builds the Telegram HTML card sendTelegram tries first —
// the same story as the plain Render body and the email card, formatted with
// Telegram's small parse_mode=HTML tag set. The header uses inline <code> for
// short tokens (repo@branch, the commit sha); the per-path lines render
// inside a single <pre> code block, omitted entirely when there are no
// paths. Every push-controlled value (message, reason, author, path,
// repository, ref, and the per-path status word) passes through esc(); the
// card's own tags and glyphs are Go constants, never interpolated.
func RenderTelegramHTML(s Summary) string {
	glyph, word := telegramVerdict(s.Status)
	branch := strings.TrimPrefix(s.Ref, "refs/heads/")

	var b strings.Builder
	b.WriteString("<b>" + glyph + " " + esc(word) + "</b>\n")
	b.WriteString("<code>" + esc(s.Repository) + "@" + esc(branch) + "</code>")
	if host := hostname(); host != "" {
		b.WriteString(" on " + esc(host))
	}
	b.WriteString("\n")

	if s.SHA != "" {
		b.WriteString("commit <code>" + esc(short(s.SHA)) + "</code>")
		if s.Author != "" {
			b.WriteString(" by " + esc(s.Author))
		}
		b.WriteString("\n")
	}
	if s.Message != "" {
		b.WriteString("<i>" + esc(firstLine(s.Message)) + "</i>\n")
	}
	if s.Error != "" {
		b.WriteString(esc(s.Error) + "\n")
	}

	if len(s.Paths) > 0 {
		// <pre> is already monospace — no nested <code> per path. Every
		// interpolated value still passes through esc(); Telegram still
		// HTML-parses the content of a <pre> block.
		b.WriteString("\n<pre>")
		for i, p := range s.Paths {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(telegramPathGlyph(p.Status) + " " + esc(p.Path))
			if p.RanAsRoot {
				// Push access to this repository is root on this server.
				b.WriteString(" ⚠ root")
			} else if p.User != "" {
				b.WriteString(" (" + esc(p.User) + ")")
			}
			if p.Status != "success" {
				b.WriteString(" " + esc(p.Status))
			}
			if p.Reason != "" {
				b.WriteString("\n   └ " + esc(p.Reason))
			}
		}
		b.WriteString("</pre>")
	}

	b.WriteString("\n\nrec-deploy " + esc(buildinfo.Resolved()))

	return b.String()
}
