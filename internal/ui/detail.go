package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Detail displays a read-only set of labelled values until the operator goes
// back or quits the interactive session.
type Detail struct {
	Title string
	Rows  [][2]string
}

// Run displays the detail view.
func (d Detail) Run() error {
	if Quitting() {
		return ErrQuit
	}
	res, err := tea.NewProgram(detailModel{Detail: d}).Run()
	if err != nil {
		return err
	}
	if res.(detailModel).quit {
		requestQuit()
		return ErrQuit
	}
	return ErrBack
}

type detailModel struct {
	Detail
	width    int
	showHelp bool
	closing  bool
	quit     bool
}

func (m detailModel) Init() tea.Cmd { return nil }

func (m detailModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = size.Width
		return m, nil
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
	if key.String() == "h" {
		m.showHelp = !m.showHelp
	}
	return m, nil
}

func (m detailModel) View() string {
	if m.closing {
		return ""
	}
	var b strings.Builder
	b.WriteString(render(StyleTitle, m.Title) + "\n\n")
	if m.width > 0 {
		b.WriteString(TwoColWidth(m.Rows, m.width))
	} else {
		b.WriteString(TwoCol(m.Rows))
	}
	if m.showHelp {
		b.WriteString("\n" + HelpPanel("keys", [][2]string{{"enter / esc / ←", "back"}, {"q / ctrl+c", "quit"}}, nil))
	} else {
		b.WriteString("\n" + render(StyleSubtle, "enter/"+navigationFooter(navigationDetail)+" • h help") + "\n")
	}
	return b.String()
}
