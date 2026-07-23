package ui

import (
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
	closing bool
	quit    bool
}

func (m documentModel) Init() tea.Cmd { return nil }

func (m documentModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	return m, nil
}

func (m documentModel) View() string {
	if m.closing {
		return ""
	}
	var b strings.Builder
	b.WriteString(render(StyleTitle, m.Title) + "\n\n")
	b.WriteString(m.Body + "\n")
	b.WriteString("\n" + render(StyleSubtle, "enter/"+navigationFooter(navigationDetail)) + "\n")
	return b.String()
}
