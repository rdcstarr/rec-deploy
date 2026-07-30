package ui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestDetailViewRendersOneBreadcrumbAndRows(t *testing.T) {
	m := detailModel{Detail: Detail{Title: "rec-deploy / MCP / Status", Rows: [][2]string{{"remote", "off"}}}, width: 80}
	view := m.View().Content
	if strings.Count(view, "rec-deploy / MCP / Status") != 1 || !strings.Contains(view, "remote") || !strings.Contains(view, "off") {
		t.Errorf("unexpected detail view:\n%s", view)
	}
}

func TestDetailNavigation(t *testing.T) {
	model, cmd := (detailModel{}).Update(keyPress("enter"))
	if cmd == nil || !model.(detailModel).closing || model.(detailModel).quit {
		t.Error("enter did not navigate back cleanly")
	}
	model, cmd = (detailModel{}).Update(keyPress("esc"))
	if cmd == nil || !model.(detailModel).closing || model.(detailModel).quit {
		t.Error("escape did not navigate back cleanly")
	}
	model, cmd = (detailModel{}).Update(keyPress("ctrl+c"))
	if cmd == nil || !model.(detailModel).quit {
		t.Error("ctrl+c did not quit")
	}
}

// A read-only view still has to be able to act on what it reports — the status
// screen that knows the endpoint is unreachable is the one that must restart it.
func TestDetailActionKeysExitWithTheKey(t *testing.T) {
	d := Detail{Keys: []Key{{Key: "s", Help: "restart service"}}}

	model, cmd := (detailModel{Detail: d}).Update(keyPress("s"))
	if cmd == nil || model.(detailModel).key != "s" || !model.(detailModel).closing {
		t.Errorf("s did not exit as an action key: %+v", model)
	}

	// An unbound key is inert, and h stays the help toggle.
	model, cmd = (detailModel{Detail: d}).Update(keyPress("x"))
	if cmd != nil || model.(detailModel).key != "" {
		t.Error("an unbound key exited the view")
	}
	if view := (detailModel{Detail: d}).View().Content; !strings.Contains(view, "s restart service") {
		t.Errorf("the footer does not advertise the action key:\n%s", view)
	}
}

// detailRows builds n labelled rows for the windowing tests below.
func detailRows(n int) [][2]string {
	rows := make([][2]string, n)
	for i := range rows {
		rows[i] = [2]string{"key-" + strconv.Itoa(i), "value-" + strconv.Itoa(i)}
	}

	return rows
}

// TestDetailScrolls pins that a detail view with more rows than the terminal
// shows a window the arrow, page and home/end keys move, that the footer says
// so, and that an action key still exits with its key while the view is
// scrolled — a read-only screen that can act on what it reports must keep doing
// so once it is long enough to page.
func TestDetailScrolls(t *testing.T) {
	SetColor(false)

	d := Detail{Title: "status", Rows: detailRows(60), Keys: []Key{{Key: "s", Help: "restart service"}}}
	var m tea.Model = detailModel{Detail: d}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 14})

	view := m.(detailModel).View().Content
	if !strings.Contains(view, "key-0") {
		t.Errorf("the first row is not shown before scrolling:\n%s", view)
	}
	if strings.Contains(view, "key-59") {
		t.Errorf("every row is rendered despite the height:\n%s", view)
	}
	if !strings.Contains(view, "↑/↓ scroll") {
		t.Errorf("a windowed detail view does not advertise that it scrolls:\n%s", view)
	}
	if !strings.Contains(view, "s restart service") {
		t.Errorf("windowing dropped the action key from the footer:\n%s", view)
	}

	// A page moves by exactly what PgDn moves, so space must land identically —
	// the chord Bubble Tea v1 spelled " " and v2 spells "space".
	paged, _ := m.Update(keyPress("space"))
	pgdn, _ := m.Update(keyPress("pgdown"))
	if got, want := paged.(detailModel).top, pgdn.(detailModel).top; got == 0 || got != want {
		t.Errorf("space moved to line %d, PgDn to %d — they must page by the same amount", got, want)
	}

	ended, _ := m.Update(keyPress("G"))
	view = ended.(detailModel).View().Content
	if !strings.Contains(view, "key-59") {
		t.Errorf("G never reaches the end:\n%s", view)
	}
	if strings.Contains(view, "key-0 ") {
		t.Errorf("the rows did not scroll:\n%s", view)
	}
	if home, _ := ended.Update(keyPress("g")); home.(detailModel).top != 0 {
		t.Error("g did not return to the first row")
	}

	// Scrolling must not cost the view its reason to exist: the action keys.
	acted, cmd := ended.Update(keyPress("s"))
	if cmd == nil || acted.(detailModel).key != "s" || !acted.(detailModel).closing {
		t.Errorf("s stopped acting once the view scrolled: %+v", acted)
	}
}

// TestDetailUnsizedRendersUnchanged pins that a view that has never received a
// WindowSizeMsg renders byte-identically to what it rendered before it could
// scroll: the same footer, the caller's help block whole, and the trailing
// newline that makes the output read as a finished block of text.
func TestDetailUnsizedRendersUnchanged(t *testing.T) {
	SetColor(false)

	d := Detail{Title: "status", Rows: [][2]string{{"access", "off"}}, Keys: []Key{{Key: "s", Help: "restart service"}}}

	closed := detailModel{Detail: d, width: 80}
	want := render(StyleTitle, "status") + "\n\n" + TwoColWidth(d.Rows, 80) +
		"\n" + render(StyleSubtle, closed.help()) + "\n"
	if got := closed.View().Content; got != want {
		t.Errorf("unsized detail view changed:\ngot:  %q\nwant: %q", got, want)
	}

	d.Help = "line-a\nline-b"
	open := detailModel{Detail: d, width: 80, showHelp: true}
	want = render(StyleTitle, "status") + "\n\n" + TwoColWidth(d.Rows, 80) + "\n" + d.Help + "\n"
	if got := open.View().Content; got != want {
		t.Errorf("unsized detail view with help open changed:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestDetailFrameFitsTheTerminal pins the detail view's share of the overflow
// the document and picker views had: detailModel tracked only its width, so it
// rendered its title, every row and the caller's whole help block regardless of
// the height it was given. Under Bubble Tea v2 a frame one row too tall loses
// its top row and defeats the renderer's unchanged-frame check — the frame area
// and the screen buffer can never match again — so the renderer repaints an
// identical screen at full framerate with no input at all. Measured on a 16-row
// pty before this pin: 180 full-screen clears and 160KB written in three idle
// seconds on `rec-deploy mcp status` with help open; after it, zero.
//
// The caller's help is swept too, because cmd/help.go hands ui.Detail a whole
// command's help: a block taller than the terminal cannot be compensated for by
// windowing the rows above it, so it has to be bounded in its own right.
func TestDetailFrameFitsTheTerminal(t *testing.T) {
	SetColor(false)

	oversized := make([]string, 60)
	for i := range oversized {
		oversized[i] = "help-" + strconv.Itoa(i)
	}

	for _, callerHelp := range []string{"", strings.Join(oversized, "\n")} {
		for _, showHelp := range []bool{false, true} {
			// Five rows is the floor a detail view can *fit*: a title, its
			// blank line, one row, and a footer with its own blank line. Below
			// it there is no arithmetic that helps, so clampFrame crops the
			// frame to the terminal — and the sweep goes all the way down to
			// one row to hold it to that.
			for height := 1; height <= 40; height++ {
				for n := 1; n <= height+2; n++ {
					var m tea.Model = detailModel{
						Detail:   Detail{Title: "status", Rows: detailRows(n), Help: callerHelp},
						showHelp: showHelp,
					}
					m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: height})

					if got := frameHeight(m.(detailModel).View().Content); got > height {
						t.Fatalf("%d rows on a %d-row terminal (help=%v, caller help=%d lines) rendered a %d-row frame",
							n, height, showHelp, strings.Count(callerHelp, "\n")+1, got)
					}
				}
			}
		}
	}
}
