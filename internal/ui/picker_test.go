package ui

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPickerActionReordersAndFollowsCursor verifies that an Action returning a
// reordered list moves the highlighted item and the cursor follows it — the
// behavior behind "favoriting a host bubbles it to the top".
func TestPickerActionReordersAndFollowsCursor(t *testing.T) {
	opts := []Option{
		{Label: "a", Value: "a"},
		{Label: "b", Value: "b"},
		{Label: "c", Value: "c"},
	}

	act := &Action{
		Key: "f",
		Run: func(v string) ([]Option, error) {
			reordered := []Option{{Label: v, Value: v}}
			for _, o := range opts {
				if o.Value != v {
					reordered = append(reordered, o)
				}
			}
			return reordered, nil
		},
	}

	m := pickerModel{Picker: Picker{Options: opts, Action: act}, cursor: 2} // on "c"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	got := next.(pickerModel)

	if got.Options[0].Value != "c" {
		t.Errorf("acted item not moved to front: %+v", got.Options)
	}
	if got.cursor != 0 {
		t.Errorf("cursor did not follow acted item to the top, got %d", got.cursor)
	}
}

// TestPickerExitKeyReportsKey verifies that an exit Key selects the highlighted
// item and reports which key fired — the behavior behind "e=edit, d=delete".
func TestPickerExitKeyReportsKey(t *testing.T) {
	opts := []Option{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}}
	p := Picker{Options: opts, Keys: []Key{{Key: "e", Help: "edit"}}}

	m := pickerModel{Picker: p, cursor: 1} // on "b"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	got := next.(pickerModel)

	if got.key != "e" {
		t.Errorf("exit key not reported: got %q, want %q", got.key, "e")
	}
	if got.chosen != "b" {
		t.Errorf("exit key chose wrong value: got %q, want %q", got.chosen, "b")
	}
	if !got.quitting {
		t.Error("exit key did not quit the picker")
	}
}

// TestPickerStatsRequiresExactAltChord checks that a sensitive toggle bound to
// Alt+R ignores a plain r and resets when a fresh picker model is created.
func TestPickerStatsRequiresExactAltChord(t *testing.T) {
	stats := &Stats{Key: "alt+r", Help: "reveal"}
	m := pickerModel{Picker: Picker{Options: []Option{{Label: "secret", Value: "secret"}}, Stats: stats}}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if next.(pickerModel).showStats {
		t.Fatal("plain r revealed stats bound to Alt+R")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r"), Alt: true})
	if !next.(pickerModel).showStats {
		t.Fatal("Alt+R did not reveal stats")
	}

	revealed := next.(pickerModel)
	next, _ = revealed.Update(statsTimeoutMsg{generation: revealed.statsGeneration})
	if next.(pickerModel).showStats {
		t.Fatal("stats stayed revealed after their timeout")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r"), Alt: true})
	revealed = next.(pickerModel)
	next, _ = revealed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("®")})
	if next.(pickerModel).showStats {
		t.Fatal("macOS Option+R character did not toggle stats back off")
	}

	fresh := pickerModel{Picker: m.Picker}
	if fresh.showStats {
		t.Fatal("reveal state leaked into a fresh picker")
	}
	if footer := fresh.help(); !strings.Contains(footer, "⌥R reveal") {
		t.Errorf("footer does not render Option chord: %q", footer)
	}
}

// TestPickerScrollsToKeepTheCursorVisible pins that a list longer than the
// terminal shows a window that follows the cursor: the first option leaves the
// view once the cursor has moved past the bottom edge, and the option under the
// cursor is always rendered.
func TestPickerScrollsToKeepTheCursorVisible(t *testing.T) {
	SetColor(false)

	options := make([]Option, 30)
	for i := range options {
		options[i] = Option{Label: "option-" + strconv.Itoa(i), Value: strconv.Itoa(i)}
	}

	var m tea.Model = pickerModel{Picker: Picker{Title: "list", Options: options}}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})

	for i := 0; i < 20; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}

	view := m.(pickerModel).View()
	if !strings.Contains(view, "option-20") {
		t.Errorf("the option under the cursor is not rendered:\n%s", view)
	}
	if strings.Contains(view, "option-0\n") {
		t.Errorf("the list did not scroll — option-0 is still shown:\n%s", view)
	}
	if !strings.Contains(view, "21/30") {
		t.Errorf("the footer does not show the position in a scrolled list:\n%s", view)
	}
}

// TestPickerShowsEverythingWhenItFits pins that a list shorter than the
// terminal, and a model that has never received a size, render unchanged.
func TestPickerShowsEverythingWhenItFits(t *testing.T) {
	SetColor(false)

	options := []Option{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}}

	var sized tea.Model = pickerModel{Picker: Picker{Options: options}}
	sized, _ = sized.Update(tea.WindowSizeMsg{Width: 80, Height: 40})

	for _, m := range []tea.Model{sized, pickerModel{Picker: Picker{Options: options}}} {
		view := m.(pickerModel).View()
		if !strings.Contains(view, "a") || !strings.Contains(view, "b") {
			t.Errorf("a list that fits lost an option:\n%s", view)
		}
		if strings.Contains(view, "1/2") {
			t.Errorf("a list that fits shows a scroll position:\n%s", view)
		}
	}
}

func TestPickerTruncatesRowsToTerminalWidth(t *testing.T) {
	m := pickerModel{Picker: Picker{
		Title:   "Narrow",
		Options: []Option{{Label: "a very long configuration value that cannot fit", Value: "value"}},
	}}

	next, _ := m.Update(tea.WindowSizeMsg{Width: 24, Height: 10})
	view := next.(pickerModel).View()
	if !strings.Contains(view, "…") {
		t.Errorf("narrow picker did not truncate its row:\n%s", view)
	}
}
