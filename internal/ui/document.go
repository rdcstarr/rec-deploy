package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Document displays preformatted text such as configuration or source code.
// It never wraps the body, so copying it preserves its syntax.
type Document struct {
	Title string
	Body  string
}

// Run displays the document until the operator goes back or quits.
func (d Document) Run() error {
	if Quitting() {
		return ErrQuit
	}
	res, err := tea.NewProgram(documentModel{Document: d}).Run()
	if err != nil {
		return err
	}
	if res.(documentModel).quit {
		requestQuit()
		return ErrQuit
	}
	return ErrBack
}

type documentModel struct {
	Document
	height  int
	top     int
	closing bool
	quit    bool
}

func (m documentModel) Init() tea.Cmd { return nil }

// Update implements tea.Model: Esc / ← / Enter go back, q / Ctrl+C quit, and the
// arrow, page and home/end keys move the window over a body too long to fit.
func (m documentModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.height = size.Height

		return m.clamp(), nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch navigationKey(key.String(), navigationDetail) {
	case navBack:
		m.closing = true

		return m, tea.Quit
	case navQuit:
		m.closing, m.quit = true, true

		return m, tea.Quit
	}

	switch key.String() {
	case "down", "j":
		m.top++
	case "up", "k":
		m.top--
	case "pgdown", " ":
		m.top += m.bodyRows()
	case "pgup":
		m.top -= m.bodyRows()
	case "home", "g":
		m.top = 0
	case "end", "G":
		m.top = len(m.lines())
	}

	return m.clamp(), nil
}

func (m documentModel) View() string {
	if m.closing {
		return ""
	}

	var b strings.Builder
	b.WriteString(render(StyleTitle, m.Title) + "\n\n")

	lines := m.lines()
	footer := "enter/" + navigationFooter(navigationDetail)
	if rows := m.bodyRows(); rows > 0 && rows < len(lines) {
		footer = "↑/↓ scroll • " + footer + fmt.Sprintf(" • %d-%d/%d", m.top+1, m.top+rows, len(lines))
		lines = lines[m.top : m.top+rows]
	}

	b.WriteString(strings.Join(lines, "\n") + "\n")
	b.WriteString("\n" + render(StyleSubtle, footer) + "\n")

	return b.String()
}

// lines is the body split for scrolling. It is recomputed per redraw rather than
// cached: a Document is built once by its caller, and a body large enough for
// the split to cost anything would not fit a terminal anyway.
func (m documentModel) lines() []string {
	return strings.Split(strings.TrimRight(m.Body, "\n"), "\n")
}

// bodyRows is how many body lines fit: the height minus the title, the blank
// line under it, and the footer with its own blank line. Zero means the height
// is unknown — no WindowSizeMsg has arrived — and the whole body is rendered.
func (m documentModel) bodyRows() int {
	if m.height <= 0 {
		return 0
	}

	rows := m.height - 4
	if rows < 1 {
		return 1
	}

	return rows
}

// clamp keeps the window inside the body after a scroll or a resize.
func (m documentModel) clamp() documentModel {
	rows := m.bodyRows()
	total := len(m.lines())
	if rows <= 0 || rows >= total {
		m.top = 0

		return m
	}

	if last := total - rows; m.top > last {
		m.top = last
	}
	if m.top < 0 {
		m.top = 0
	}

	return m
}
