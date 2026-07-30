package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Detail displays a read-only set of labelled values until the operator goes
// back or quits the interactive session. Nothing on it is selectable — the rows
// are the result, not a list of destinations.
type Detail struct {
	Title string
	Rows  [][2]string
	// Keys are action keys that exit the view, each reported back by RunKey so
	// the caller can branch on it. They let a screen stay read-only and still
	// act on what it reports (e.g. s=restart service on a status view).
	Keys []Key
	// Help, when set, is pre-rendered help shown when help is toggled with h
	// (e.g. a command's commands/flags); otherwise h shows the keybindings.
	Help string
}

// Run displays the detail view and discards any action key, so a view without
// Keys always ends in ErrBack or ErrQuit.
func (d Detail) Run() error {
	_, err := d.RunKey()

	return err
}

// RunKey displays the detail view and returns the action key that exited it
// along with a nil error, or "" and ErrBack when the operator went back with
// Esc, ← or Enter. It returns ErrQuit if the user quits with q or Ctrl+C.
func (d Detail) RunKey() (string, error) {
	if Quitting() {
		return "", ErrQuit
	}
	res, err := tea.NewProgram(detailModel{Detail: d}).Run()
	if err != nil {
		return "", err
	}
	final := res.(detailModel)
	if final.quit {
		requestQuit()
		return "", ErrQuit
	}
	if final.key != "" {
		return final.key, nil
	}

	return "", ErrBack
}

type detailModel struct {
	Detail
	width    int
	showHelp bool
	closing  bool
	quit     bool
	key      string
}

func (m detailModel) Init() tea.Cmd { return nil }

func (m detailModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = size.Width
		return m, nil
	}
	key, ok := msg.(tea.KeyPressMsg)
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
		return m, nil
	}
	for _, k := range m.Keys {
		if key.String() == k.Key {
			m.key, m.closing = k.Key, true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m detailModel) View() tea.View {
	if m.closing {
		return tea.NewView("")
	}
	var b strings.Builder
	b.WriteString(render(StyleTitle, m.Title) + "\n\n")
	if m.width > 0 {
		b.WriteString(TwoColWidth(m.Rows, m.width))
	} else {
		b.WriteString(TwoCol(m.Rows))
	}
	if m.showHelp {
		b.WriteString("\n" + m.helpBlock())
	} else {
		b.WriteString("\n" + render(StyleSubtle, m.help()) + "\n")
	}
	return tea.NewView(b.String())
}

// help is the footer hint line: the action keys first, then navigation.
func (m detailModel) help() string {
	hints := make([]string, 0, len(m.Keys)+2)
	for _, k := range m.Keys {
		hints = append(hints, k.Key+" "+k.Help)
	}
	hints = append(hints, "enter/"+navigationFooter(navigationDetail), "h help")

	return strings.Join(hints, " • ")
}

// helpBlock is the panel h toggles: the caller's own help when it supplied one,
// otherwise this view's keybindings.
func (m detailModel) helpBlock() string {
	if m.Help != "" {
		return m.Help + "\n"
	}

	rows := make([][2]string, 0, len(m.Keys)+3)
	for _, k := range m.Keys {
		rows = append(rows, [2]string{k.Key, k.Help})
	}
	rows = append(rows, [2]string{"enter / esc / ←", "back"}, [2]string{"q / ctrl+c", "quit"})

	return HelpPanel("keys", rows, nil)
}
