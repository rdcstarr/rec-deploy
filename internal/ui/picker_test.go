package ui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

	next, _ := m.Update(keyPress("f"))
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

	next, _ := m.Update(keyPress("e"))
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

	next, _ := m.Update(keyPress("r"))
	if next.(pickerModel).showStats {
		t.Fatal("plain r revealed stats bound to Alt+R")
	}

	next, _ = m.Update(keyPress("alt+r"))
	if !next.(pickerModel).showStats {
		t.Fatal("Alt+R did not reveal stats")
	}

	revealed := next.(pickerModel)
	next, _ = revealed.Update(statsTimeoutMsg{generation: revealed.statsGeneration})
	if next.(pickerModel).showStats {
		t.Fatal("stats stayed revealed after their timeout")
	}

	next, _ = m.Update(keyPress("alt+r"))
	revealed = next.(pickerModel)
	next, _ = revealed.Update(keyPress("®"))
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
		m, _ = m.Update(keyPress("down"))
	}

	view := m.(pickerModel).View().Content
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
		view := m.(pickerModel).View().Content
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
	view := next.(pickerModel).View().Content
	if !strings.Contains(view, "…") {
		t.Errorf("narrow picker did not truncate its row:\n%s", view)
	}
}

// TestPickerBackOutIsDistinctFromQuit pins the return contract every cmd-layer
// back-out relies on: Esc leaves the picker with an empty value and NO error,
// while Ctrl+C sets the quit flag Run turns into ErrQuit. Because a back-out and
// a real choice both carry a nil error, a caller must test the error before the
// empty value — folding them into `if err != nil || value == "" { return err }`
// returned nil on Esc, which dispatch reads as a completed command and unwinds
// the whole session. The split into err-first is what this contract forces.
func TestPickerBackOutIsDistinctFromQuit(t *testing.T) {
	opts := []Option{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}}

	esc, _ := pickerModel{Picker: Picker{Options: opts}}.Update(keyPress("esc"))
	back := esc.(pickerModel)
	if back.quit {
		t.Error("Esc set the quit flag; Run would return ErrQuit instead of a back-out")
	}
	if back.chosen != "" || back.err != nil {
		t.Errorf("Esc = {chosen:%q err:%v}, want an empty value and no error", back.chosen, back.err)
	}

	quit, _ := pickerModel{Picker: Picker{Options: opts}}.Update(keyPress("ctrl+c"))
	if !quit.(pickerModel).quit {
		t.Error("Ctrl+C did not set the quit flag; Run would not return ErrQuit")
	}
}

// TestPickerFrameFitsTheTerminal pins the picker's half of the overflow the
// document view had: a menu long enough to page rendered one row more than the
// terminal, which under Bubble Tea v2 costs the title row and a full-framerate
// repaint of an unchanged screen. Measured on a 30-row pty before this pin: 181
// full-screen clears in three idle seconds; after it, zero.
//
// TestPickerHelpTakesAtMostHalfTheScreen pins the trade the help budget makes,
// because fitting the frame is not on its own enough to make the screen good.
// Bounding the block only against the terminal let a 24-line command help take
// eighteen of a 24-row terminal's rows and squeeze the menu to one option — and
// what it spent them on was the same command list the menu underneath it was
// already showing, with only the Flags section cut. Half the screen each keeps
// both readable.
//
// The small-terminal end is pinned with it: below two spare rows the block is
// dropped rather than rendered as a lone "… N more lines", which is a row spent
// saying nothing and, at six rows, still overflowed the frame.
func TestPickerHelpTakesAtMostHalfTheScreen(t *testing.T) {
	SetColor(false)

	lines := make([]string, 24)
	for i := range lines {
		lines[i] = "help-" + strconv.Itoa(i)
	}

	opts := make([]Option, 20)
	for i := range opts {
		opts[i] = Option{Label: "opt-" + strconv.Itoa(i), Value: strconv.Itoa(i)}
	}

	p := pickerModel{Picker: Picker{Title: "menu", Options: opts, Help: strings.Join(lines, "\n")}, showHelp: true}
	sized, _ := p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p = sized.(pickerModel)

	// The block carries its own leading blank line, which is chrome, not help.
	if got := strings.Count(p.helpBlock(), "\n") - 1; got != 12 {
		t.Errorf("the help block took %d of 24 rows, want 12 — half the screen", got)
	}
	if got := p.listRows(); got != 7 {
		t.Errorf("a 24-row terminal shows %d options with help open, want 7", got)
	}
	if got := frameHeight(p.View().Content); got != 24 {
		t.Errorf("the frame is %d rows on a 24-row terminal, want exactly 24", got)
	}

	// Six rows have nothing to spare, and a block that cannot fit is not help.
	for _, height := range []int{5, 6, 7} {
		small, _ := p.Update(tea.WindowSizeMsg{Width: 80, Height: height})
		if block := small.(pickerModel).helpBlock(); block != "" {
			t.Errorf("a %d-row terminal still renders a help block: %q", height, block)
		}
		if got := frameHeight(small.(pickerModel).View().Content); got > height {
			t.Errorf("a %d-row terminal rendered a %d-row frame with help open", height, got)
		}
	}
}

// The caller's help is swept too, because cmd/help.go hands every menu the
// running command's help and root's is 24 lines: a block taller than the
// terminal cannot be compensated for by windowing the options above it — they
// bottom out at one row — so it has to be bounded in its own right.
func TestPickerFrameFitsTheTerminal(t *testing.T) {
	SetColor(false)

	oversized := make([]string, 60)
	for i := range oversized {
		oversized[i] = "help-" + strconv.Itoa(i)
	}

	for _, callerHelp := range []string{"", strings.Join(oversized, "\n")} {
		for _, showHelp := range []bool{false, true} {
			// Five rows is the floor a picker can *fit*: a title, its blank
			// line, one option, and a footer with its own blank line. Below it
			// there is no arithmetic that helps, so clampFrame crops the frame
			// to the terminal — and the sweep goes all the way down to one row
			// to hold it to that.
			for height := 1; height <= 40; height++ {
				for n := 1; n <= height+2; n++ {
					opts := make([]Option, n)
					for i := range opts {
						opts[i] = Option{Label: "opt-" + strconv.Itoa(i), Value: strconv.Itoa(i)}
					}

					var m tea.Model = pickerModel{
						Picker:   Picker{Title: "menu", Options: opts, Help: callerHelp},
						showHelp: showHelp,
					}
					m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: height})

					if got := frameHeight(m.(pickerModel).View().Content); got > height {
						t.Fatalf("%d options on a %d-row terminal (help=%v, caller help=%d lines) rendered a %d-row frame",
							n, height, showHelp, strings.Count(callerHelp, "\n")+1, got)
					}
				}
			}
		}
	}
}
