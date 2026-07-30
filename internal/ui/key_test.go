package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keyPress builds the message a terminal sends for chord, so the tests below
// state the keystroke an operator types instead of its wire encoding. It covers
// the chords rec-deploy's navigation contract uses and nothing else.
func keyPress(chord string) tea.KeyMsg {
	switch chord {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}

	if alt, ok := strings.CutPrefix(chord, "alt+"); ok {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(alt), Alt: true}
	}

	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(chord)}
}

// TestKeyPressMatchesTheNavigationContract pins that keyPress produces the exact
// strings navigationKey switches on, so a test written against a chord name
// exercises the same path a real keystroke does.
func TestKeyPressMatchesTheNavigationContract(t *testing.T) {
	for _, chord := range []string{"up", "down", "left", "enter", "esc", "ctrl+c", "q", "h", "alt+r", "®"} {
		if got := keyPress(chord).String(); got != chord {
			t.Errorf("keyPress(%q).String() = %q, want %q", chord, got, chord)
		}
	}
}
