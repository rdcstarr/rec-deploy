package ui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestDocumentScrolls pins that a body longer than the terminal shows a window
// the arrow keys move, and that the footer reports where in the body it is.
func TestDocumentScrolls(t *testing.T) {
	SetColor(false)

	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "line-" + strconv.Itoa(i)
	}
	body := strings.Join(lines, "\n")

	var m tea.Model = documentModel{Document: Document{Title: "output", Body: body}}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 14})

	view := m.(documentModel).View().Content
	if !strings.Contains(view, "line-0\n") {
		t.Errorf("the first line is not shown before scrolling:\n%s", view)
	}
	if strings.Contains(view, "line-59") {
		t.Errorf("the whole body is rendered despite the height:\n%s", view)
	}

	for i := 0; i < 50; i++ {
		m, _ = m.Update(keyPress("down"))
	}

	view = m.(documentModel).View().Content
	if !strings.Contains(view, "line-59") {
		t.Errorf("scrolling down never reaches the end:\n%s", view)
	}
	if strings.Contains(view, "line-0\n") {
		t.Errorf("the body did not scroll:\n%s", view)
	}
}

// TestDocumentPagesWithSpace pins that the space bar pages the body like PgDn.
// Bubble Tea v1 stringified the space bar as " " and v2 names it "space", so the
// paging arm silently matched nothing after the v2 bump: space still redrew the
// same window and the logs browser lost its most-used scroll key with every test
// still green.
func TestDocumentPagesWithSpace(t *testing.T) {
	SetColor(false)

	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "line-" + strconv.Itoa(i)
	}

	var m tea.Model = documentModel{Document: Document{Title: "output", Body: strings.Join(lines, "\n")}}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 14})

	paged, _ := m.Update(keyPress("space"))
	if top := paged.(documentModel).top; top == 0 {
		t.Fatal("space did not page the body — the window never left the top")
	}

	// A page is exactly what PgDn moves, so the two keys must land identically.
	pgdn, _ := m.Update(keyPress("pgdown"))
	if got, want := paged.(documentModel).top, pgdn.(documentModel).top; got != want {
		t.Errorf("space moved to line %d, PgDn to %d — they must page by the same amount", got, want)
	}

	view := paged.(documentModel).View().Content
	if strings.Contains(view, "line-0\n") {
		t.Errorf("the body did not scroll after space:\n%s", view)
	}
}

// TestDocumentUnsizedBodyKeepsTrailingNewline pins that an unsized document
// renders its title block followed by exactly Body + "\n" — lines() trims the
// body's trailing newline for windowing math, and that trim must not leak into
// what gets printed when no window applies (regression: a body ending in "\n"
// used to render one line short).
func TestDocumentUnsizedBodyKeepsTrailingNewline(t *testing.T) {
	SetColor(false)

	body := "one\ntwo\n"
	m := documentModel{Document: Document{Title: "output", Body: body}}

	want := render(StyleTitle, "output") + "\n\n" + body + "\n\n" +
		render(StyleSubtle, "enter/"+navigationFooter(navigationDetail)) + "\n"
	if got := m.View().Content; got != want {
		t.Errorf("unsized document with trailing newline rendered wrong:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestDocumentShowsEverythingWhenItFits pins that a body shorter than the
// terminal, and a model that has never received a size, render unchanged.
func TestDocumentShowsEverythingWhenItFits(t *testing.T) {
	SetColor(false)

	m := documentModel{Document: Document{Title: "output", Body: "one\ntwo"}}
	if view := m.View().Content; !strings.Contains(view, "one") || !strings.Contains(view, "two") {
		t.Errorf("an unsized document lost a line:\n%s", view)
	}

	sized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	if view := sized.(documentModel).View().Content; !strings.Contains(view, "one") || !strings.Contains(view, "two") {
		t.Errorf("a document that fits lost a line:\n%s", view)
	}
}
