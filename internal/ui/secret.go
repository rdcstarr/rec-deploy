package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// SecretDetail displays one secret as a read-only masked value. Alt+R reveals
// it only for the lifetime of this view; the value cannot be edited here.
type SecretDetail struct {
	Title string
	Label string
	Value string
}

// Run displays the read-only secret view.
func (d SecretDetail) Run() error {
	if Quitting() {
		return ErrQuit
	}
	res, err := tea.NewProgram(secretDetailModel{SecretDetail: d}).Run()
	if err != nil {
		return err
	}
	if res.(secretDetailModel).quit {
		requestQuit()
		return ErrQuit
	}
	return ErrBack
}

type secretDetailModel struct {
	SecretDetail
	// height is tracked only so clampFrame can crop. This view renders one
	// value and is six rows tall whatever the terminal does, so it has no
	// windowing to do and nothing to fit; below six rows there is simply not
	// room for it, and the frame has to be cropped rather than made smaller.
	height   int
	revealed bool
	closing  bool
	quit     bool
}

func (m secretDetailModel) Init() tea.Cmd { return nil }

func (m secretDetailModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.height = size.Height

		return m, nil
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if matchesPickerKey(key.String(), "alt+r") {
		m.revealed = !m.revealed
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

func (m secretDetailModel) View() tea.View {
	if m.closing {
		return tea.NewView("")
	}
	value := "********"
	action := "reveal"
	if m.revealed {
		value = m.Value
		action = "mask"
	}
	var b strings.Builder
	b.WriteString(render(StyleTitle, m.Title) + "\n\n")
	b.WriteString(TwoCol([][2]string{{m.Label, value}}))
	b.WriteString("\n" + render(StyleSubtle, "⌥R "+action+" • enter/"+navigationFooter(navigationDetail)) + "\n")
	return tea.NewView(clampFrame(b.String(), m.height))
}
