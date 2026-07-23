// Package ui centralises rec-deploy's terminal look-and-feel: the lipgloss
// palette and the shared renderers every command uses, so output stays
// consistent across the whole CLI.
package ui

import "github.com/charmbracelet/lipgloss"

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

// SetColor enables or disables ANSI styling across all rendering. It is
// driven by --no-color / NO_COLOR; when enabled, lipgloss keeps its own
// TTY-aware auto-detection.
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
