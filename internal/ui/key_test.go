package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// keyPress builds the message a terminal sends for chord, so the tests below
// state the keystroke an operator types instead of its wire encoding. It covers
// the chords rec-deploy's navigation contract uses and nothing else.
func keyPress(chord string) tea.KeyMsg {
	switch chord {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}

	// A modified chord carries no Text: Key.String returns Text verbatim when it
	// is set, so filling it in would stringify Alt+R as a plain "r" and the
	// reveal chord would match a bare keypress.
	if alt, ok := strings.CutPrefix(chord, "alt+"); ok {
		return tea.KeyPressMsg{Code: []rune(alt)[0], Mod: tea.ModAlt}
	}

	return tea.KeyPressMsg{Code: []rune(chord)[0], Text: chord}
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
