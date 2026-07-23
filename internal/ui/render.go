package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// Out writes a line to stdout — the user-facing output channel.
func Out(s string) {
	fmt.Fprintln(os.Stdout, s)
}

// Outf writes a formatted line to stdout.
func Outf(format string, a ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", a...)
}

// Title prints a bold, highlighted title line.
func Title(s string) {
	Out(render(StyleTitle, s))
}

// ScreenPath builds the consistent breadcrumb used by nested interactive
// screens, for example "rec-deploy / Config / Email".
func ScreenPath(parts ...string) string {
	return strings.Join(parts, " / ")
}

// Step prints a wizard step heading: one blank line, then "[n/total] Name". It
// is deliberately the only place a wizard emits a blank line, so every step is
// separated by the same amount of space.
func Step(n, total int, name string) {
	Out("")
	Out(render(StyleTitle, fmt.Sprintf("[%d/%d] %s", n, total, name)))
}

// keyWidth is the padded width of the key column in KeyValue / KeyList. It has
// to fit the longest key in the tree — "auto-update:" is 12 — or that row's
// value lands a column right of every other one. A longer key added later
// needs this widened with it.
const keyWidth = 12

// KeyValueLine returns the aligned "key: value" line KeyValue prints, for
// composing a block rather than writing it a line at a time.
func KeyValueLine(key, value string) string {
	return render(StyleKey, fmt.Sprintf("%-*s", keyWidth, key+":")) + " " + value
}

// KeyValue prints an aligned "key: value" line with a subtle key.
func KeyValue(key, value string) {
	Out(KeyValueLine(key, value))
}

// KeyList prints a labelled list: the key on the first line, remaining values
// aligned underneath it.
func KeyList(key string, values []string) {
	for i, v := range values {
		if i == 0 {
			KeyValue(key, v)
			continue
		}

		Outf("%s %s", strings.Repeat(" ", keyWidth), v)
	}
}

// Success prints a success message to stdout.
func Success(s string) {
	Out(render(StyleSuccess, "✓") + " " + s)
}

// Warn prints a warning message to stdout.
func Warn(s string) {
	Out(render(StyleWarn, "!") + " " + s)
}

// Info prints a subtle informational message to stdout.
func Info(s string) {
	Out(render(StyleSubtle, s))
}

// Dim returns s in the subtle (dimmer) palette color, for secondary text such
// as inline descriptions next to a brighter label. It honors --no-color.
func Dim(s string) string {
	return render(StyleSubtle, s)
}

// Good returns s in the success (green) palette color, honoring --no-color.
func Good(s string) string {
	return render(StyleSuccess, s)
}

// Alert returns s in the warning palette color, honoring --no-color. It is the
// string form of Warn, for composing a block rather than printing a line.
func Alert(s string) string {
	return render(StyleWarn, s)
}

// Accent returns s in the highlight palette color, honoring --no-color.
func Accent(s string) string {
	return render(StyleHighlight, s)
}

// Heading returns s as a bold section title, honoring --no-color.
func Heading(s string) string {
	return render(StyleTitle, s)
}

// TwoCol renders aligned "name  description" rows: name highlighted, description
// dimmed, the name column sized to the widest name (display width, not bytes).
func TwoCol(rows [][2]string) string {
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return TwoColWidth(rows, width)
	}

	width := 0
	for _, r := range rows {
		if w := lipgloss.Width(r[0]); w > width {
			width = w
		}
	}

	var b strings.Builder
	for _, r := range rows {
		pad := width - lipgloss.Width(r[0]) + 2
		b.WriteString("  " + render(StyleHighlight, r[0]) + strings.Repeat(" ", pad) + render(StyleSubtle, r[1]) + "\n")
	}

	return b.String()
}

// TwoColWidth renders rows within a known terminal width. Long descriptions
// wrap under their own column and long keys are truncated without breaking
// ANSI sequences or wide Unicode glyphs.
func TwoColWidth(rows [][2]string, width int) string {
	if width < 24 {
		width = 24
	}
	keyWidth := 0
	for _, row := range rows {
		if w := lipgloss.Width(row[0]); w > keyWidth {
			keyWidth = w
		}
	}
	maxKeyWidth := width / 3
	if keyWidth > maxKeyWidth {
		keyWidth = maxKeyWidth
	}
	descWidth := width - keyWidth - 4
	if descWidth < 8 {
		descWidth = 8
	}

	var b strings.Builder
	for _, row := range rows {
		key := ansi.Truncate(row[0], keyWidth, "…")
		description := ansi.Wordwrap(row[1], descWidth, " /")
		lines := strings.Split(description, "\n")
		for i, line := range lines {
			if i == 0 {
				pad := keyWidth - lipgloss.Width(key) + 2
				b.WriteString("  " + render(StyleHighlight, key) + strings.Repeat(" ", pad))
			} else {
				b.WriteString(strings.Repeat(" ", keyWidth+4))
			}
			b.WriteString(render(StyleSubtle, line) + "\n")
		}
	}

	return b.String()
}

// HelpPanel renders a keybinding/examples help block, shared by interactive
// views. rows are {key, description} pairs; examples is an optional list of
// usage lines shown under an "examples" heading.
func HelpPanel(title string, rows [][2]string, examples []string) string {
	var b strings.Builder

	b.WriteString(render(StyleTitle, title) + "\n")
	b.WriteString(TwoCol(rows))

	if len(examples) > 0 {
		b.WriteString("\n" + render(StyleSubtle, "examples") + "\n")
		for _, e := range examples {
			b.WriteString("  " + render(StyleSubtle, e) + "\n")
		}
	}

	return b.String()
}

// ProgressBar renders a fixed-width bar for the fraction frac in [0,1]: a filled
// highlight segment over a subtle remainder.
func ProgressBar(frac float64, width int) string {
	if width < 1 {
		width = 1
	}
	if frac < 0 {
		frac = 0
	} else if frac > 1 {
		frac = 1
	}

	filled := int(frac*float64(width) + 0.5)
	if filled > width {
		filled = width
	}

	return render(StyleHighlight, strings.Repeat("█", filled)) + render(StyleSubtle, strings.Repeat("░", width-filled))
}

// Star returns a colored favorite marker ("★"), honoring --no-color.
func Star() string {
	return render(StyleWarn, "★")
}

// RenderError prints err as a clean, single-line message on stderr. The
// navigation signals ErrQuit and ErrBack are not real errors and are never
// rendered, so a menu loop can pass them to RenderError harmlessly: ErrQuit is
// then caught by the next Quitting() check, ErrBack just unwinds to this loop.
func RenderError(err error) {
	if err == nil || IsQuit(err) || errors.Is(err, ErrBack) {
		return
	}

	fmt.Fprintln(os.Stderr, render(StyleError, "error:")+" "+err.Error())
}

// PrintJSON marshals v as indented JSON to stdout. Commands use it when the
// global --json flag is set, so machine-readable output is consistent.
func PrintJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	return enc.Encode(v)
}
