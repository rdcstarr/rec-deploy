package notify

import (
	"html/template"
	"strconv"
	"strings"

	"github.com/rdcstarr/rec-deploy/internal/buildinfo"
)

// emailTemplate is the "Consolă" card: the deploy rendered as a terminal
// window, mirroring the CLI's glyphs and colors. Table layout and inline
// styles only — Outlook renders with Word's engine; no images, no web fonts,
// no <style> block, so the card survives every client untouched.
const emailTemplate = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="color-scheme" content="dark"><title>{{.Subject}}</title></head>
<body style="margin:0;padding:0;background:#1a1d21">
<table width="100%" cellpadding="0" cellspacing="0" style="background:#1a1d21"><tr><td align="center" style="padding:28px 12px">
<table width="600" cellpadding="0" cellspacing="0" style="max-width:600px;width:100%;background:#0e1114;border:1px solid {{.Border}};border-radius:10px;border-collapse:separate;overflow:hidden">
<tr><td style="padding:11px 16px;background:{{.ChromeBG}};border-bottom:1px solid {{.Border}}">
<span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:#d05a4e"></span>
<span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:#e0a63f;margin-left:5px"></span>
<span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:#39a862;margin-left:5px"></span>
<span style="font:11px ui-monospace,'Cascadia Mono',Consolas,'Liberation Mono',monospace;color:#6d7278;padding-left:12px">rec-deploy — webhook{{with .Host}} · {{.}}{{end}}</span>
</td></tr>
<tr><td style="padding:24px 26px 26px;font:13.5px/1.85 ui-monospace,'Cascadia Mono',Consolas,'Liberation Mono',monospace;color:#c9d1d9">
<span style="color:#6d7278">$ push {{with .SHA7}}{{.}} {{end}}→ {{.Repository}}@{{.Branch}}</span><br>
{{if .SHA7}}<span style="color:#58a6ff">›</span> commit <span style="color:#e0a63f">{{.SHA7}}</span>{{with .Author}} by {{.}}{{end}}<br>
{{end}}{{with .MessageLine}}<span style="color:#58a6ff">›</span> <span style="color:#8b949e">{{.}}</span><br>
{{end}}{{with .Error}}<span style="color:#f2635a">! {{.}}</span><br>
{{end}}<br>
{{range $p := .Paths}}<span style="color:{{$p.Color}}">{{$p.Glyph}}</span> {{$p.Path}}{{if $p.RanAsRoot}} <span style="color:#e0a63f">⚠ root</span>{{else if $p.User}} <span style="color:#6d7278">({{$p.User}})</span>{{end}}{{with $p.StatusWord}} <span style="color:{{$p.Color}}">{{.}}</span>{{end}}<br>
{{with $p.Reason}}<span style="color:#8a5a54">&nbsp;&nbsp;└ {{.}}</span><br>
{{end}}{{end}}<br>
<span style="color:{{.VerdictColor}};font-weight:bold">{{.VerdictGlyph}} {{.VerdictWord}}</span> <span style="color:#6d7278">— {{.Tail}}{{if .JournalHint}}; see journalctl -u rec-deploy{{end}}</span><br>
<span style="color:#3d434a">— rec-deploy {{.Version}}{{with .Host}} · {{.}}{{end}}</span>
</td></tr>
</table>
</td></tr></table>
</body></html>`

var emailTmpl = template.Must(template.New("email").Parse(emailTemplate))

// emailView is emailTemplate's data: Summary reshaped into the exact strings
// the card prints, so the template stays free of logic beyond conditionals.
type emailView struct {
	Subject      string
	Repository   string
	Branch       string
	SHA7         string
	Author       string
	MessageLine  string
	Error        string
	Version      string
	Host         string
	VerdictGlyph string // "✓" | "!" | "›"
	VerdictColor string // green | red | amber
	VerdictWord  string // "deployed" | "failed" | the raw status
	JournalHint  bool   // only on failure
	Failed       bool   // failure livery: border + chrome colors
	Tail         string
	Border       string
	ChromeBG     string
	Paths        []emailPathView
}

// emailPathView is one installation line: glyph and color are pre-picked in Go
// so the template never branches on status strings.
type emailPathView struct {
	Path       string
	User       string
	StatusWord string
	Reason     string
	Glyph      string
	Color      string
	RanAsRoot  bool
}

// RenderHTML builds the HTML body the email channel sends — the deploy as a
// terminal window, mirroring what the CLI prints. Every dynamic value passes
// through html/template's contextual escaping: commit messages, reasons and
// paths are push-controlled input, never trusted.
func RenderHTML(s Summary) (string, error) {
	v := emailView{
		Subject:     Subject(s),
		Repository:  s.Repository,
		Branch:      strings.TrimPrefix(s.Ref, "refs/heads/"),
		SHA7:        short(s.SHA),
		Author:      s.Author,
		MessageLine: firstLine(s.Message),
		Error:       s.Error,
		Version:     buildinfo.Resolved(),
		Border:      "#23272c",
		ChromeBG:    "#161a1e",
	}
	switch s.Status {
	case "success":
		v.VerdictGlyph, v.VerdictColor, v.VerdictWord = "✓", "#39d47f", "deployed"
	case "failed":
		v.VerdictGlyph, v.VerdictColor, v.VerdictWord = "!", "#f2635a", "failed"
		v.JournalHint = true
		v.Failed = true
	default: // "skipped", "test", anything neutral — no failure livery
		v.VerdictGlyph, v.VerdictColor, v.VerdictWord = "›", "#e0a63f", s.Status
	}
	if v.Failed {
		v.Border, v.ChromeBG = "#3a2724", "#1a1416"
	}
	v.Host = hostname()

	failed := 0
	for _, p := range s.Paths {
		pv := emailPathView{
			Path:      p.Path,
			User:      p.User,
			Reason:    p.Reason,
			RanAsRoot: p.RanAsRoot,
		}
		switch p.Status {
		case "success":
			pv.Glyph, pv.Color = "✓", "#39d47f"
		case "failed", "rolled_back":
			pv.Glyph, pv.Color = "✗", "#f2635a"
			pv.StatusWord = p.Status
			failed++
		default:
			pv.Glyph, pv.Color = "›", "#e0a63f"
			pv.StatusWord = p.Status
		}
		v.Paths = append(v.Paths, pv)
	}

	total := len(s.Paths)
	if v.Failed && failed > 0 {
		v.Tail = strconv.Itoa(failed) + " of " + strconv.Itoa(total) + " installations failed"
	} else {
		v.Tail = strconv.Itoa(total) + " installation"
		if total != 1 {
			v.Tail += "s"
		}
	}

	var b strings.Builder
	if err := emailTmpl.Execute(&b, v); err != nil {
		return "", err
	}

	return b.String(), nil
}
