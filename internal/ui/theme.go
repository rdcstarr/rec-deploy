// Package ui centralises rec-deploy's terminal look-and-feel: the lipgloss
// palette and the shared renderers every command uses, so output stays
// consistent across the whole CLI.
package ui

import "charm.land/lipgloss/v2"

// Shared lipgloss styles, kept in one place so every command renders with the
// same palette.
var (
	StyleTitle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	StyleSubtle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	StyleSuccess   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	StyleWarn      = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	StyleError     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	StyleHighlight = lipgloss.NewStyle().Foreground(lipgloss.Color("69"))
	StyleKey       = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// colorEnabled gates ANSI styling; toggled by SetColor.
var colorEnabled = true

// SetColor enables or disables ANSI styling across all rendering. It is driven
// by --no-color / NO_COLOR, and disabling it is the only thing that keeps a
// style from being emitted at all: since lipgloss v2, Style.Render always writes
// the full escape sequence, and the reduction to what the terminal actually
// supports — down to no colour at all when the destination is not a TTY —
// happens at write time, inside lipgloss.Fprint and its siblings. Styled output
// therefore has to leave through Out, Outf or Print; a raw fmt.Print bypasses
// the only step that would have adapted it.
func SetColor(enabled bool) {
	colorEnabled = enabled
}

// render applies a lipgloss style unless color has been disabled.
func render(style lipgloss.Style, s string) string {
	if !colorEnabled {
		return s
	}

	return style.Render(s)
}
