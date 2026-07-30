package ui

import (
	"fmt"
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
	height   int
	top      int
	showHelp bool
	closing  bool
	quit     bool
	key      string
}

func (m detailModel) Init() tea.Cmd { return nil }

// Update implements tea.Model: Esc / ← / Enter go back, q / Ctrl+C quit, h
// toggles the help block, an action Key exits reporting itself, and the arrow,
// page and home/end keys move the window over rows too long to fit.
func (m detailModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = size.Width, size.Height

		return m.clamp(), nil
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

		// Opening the block takes rows away from the body, so the window it
		// leaves may no longer reach where the view was scrolled to.
		return m.clamp(), nil
	}
	for _, k := range m.Keys {
		if key.String() == k.Key {
			m.key, m.closing = k.Key, true
			return m, tea.Quit
		}
	}

	// Scrolling is matched after the action Keys so a caller that binds one of
	// these chords keeps it: the view exists to report and to act on what it
	// reports, and paging must not take that away.
	switch key.String() {
	case "down", "j":
		m.top++
	case "up", "k":
		m.top--
	case "pgdown", "space":
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

func (m detailModel) View() tea.View {
	if m.closing {
		return tea.NewView("")
	}

	var b strings.Builder
	b.WriteString(render(StyleTitle, m.Title) + "\n\n")

	// body defaults to the untouched two-column block so a view that fits, or
	// one that has never received a WindowSizeMsg, renders byte-identically to
	// what it rendered before it could scroll — lines() trims the block's
	// trailing newline for the windowing math and must not leak that trim into
	// what gets printed when no window applies.
	lines := m.lines()
	body, scroll := m.rowsBlock(), ""
	if rows := m.bodyRows(); rows > 0 && rows < len(lines) {
		body = strings.Join(lines[m.top:m.top+rows], "\n") + "\n"
		scroll = fmt.Sprintf(" • %d-%d/%d", m.top+1, m.top+rows, len(lines))
	}
	b.WriteString(body)

	// The help block replaces the footer rather than joining it, and carries its
	// own trailing newline; frameEnd owns that last row instead, so a sized view
	// stops one row short of the height it was given. A block too big for the
	// terminal to hold is empty, and the footer stands in for it.
	if block := m.helpBlock(); block != "" {
		b.WriteString("\n" + strings.TrimSuffix(block, "\n") + frameEnd(m.height))

		return tea.NewView(b.String())
	}

	footer := m.help()
	if scroll != "" {
		footer = "↑/↓ scroll • " + footer + scroll
	}
	b.WriteString("\n" + render(StyleSubtle, footer) + frameEnd(m.height))

	return tea.NewView(b.String())
}

// rowsBlock is the two-column body, sized to the terminal once a WindowSizeMsg
// has set the width.
func (m detailModel) rowsBlock() string {
	if m.width > 0 {
		return TwoColWidth(m.Rows, m.width)
	}

	return TwoCol(m.Rows)
}

// lines is the body split for scrolling. It windows rendered lines rather than
// Rows because TwoColWidth wraps a long value onto several of them, so one row
// is not one row of the terminal. Like documentModel's, it is recomputed per
// redraw rather than cached: a Detail is built once by its caller, and a body
// large enough for the split to cost anything would not fit a terminal anyway.
func (m detailModel) lines() []string {
	return strings.Split(strings.TrimRight(m.rowsBlock(), "\n"), "\n")
}

// bodyRows is how many body lines fit: the height minus the chrome View draws
// around them. Zero means the height is unknown — no WindowSizeMsg has arrived,
// as in a test — and every row is rendered.
func (m detailModel) bodyRows() int {
	if m.height <= 0 {
		return 0
	}

	rows := m.height - m.chromeLines()
	if rows < 1 {
		return 1
	}

	return rows
}

// chromeLines is how many rows View writes around the body: the title, the
// blank line under it, and the footer with its own blank line — or, when a help
// block is being shown, that blank line plus the block, which replaces the
// footer.
func (m detailModel) chromeLines() int {
	block := m.helpBlock()
	if block == "" {
		return 4
	}

	return 3 + strings.Count(block, "\n")
}

// clamp keeps the window inside the body after a scroll or a resize.
func (m detailModel) clamp() detailModel {
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
// otherwise this view's keybindings. It is the single source of truth for the
// block — empty when help is closed or when it cannot be shown — so chromeLines
// measures exactly what View writes, which is where the original overflow came
// from.
func (m detailModel) helpBlock() string {
	if !m.showHelp {
		return ""
	}

	block := m.Help + "\n"
	if m.Help == "" {
		rows := make([][2]string, 0, len(m.Keys)+3)
		for _, k := range m.Keys {
			rows = append(rows, [2]string{k.Key, k.Help})
		}
		rows = append(rows, [2]string{"enter / esc / ←", "back"}, [2]string{"q / ctrl+c", "quit"})
		block = HelpPanel("keys", rows, nil)
	}

	// An unsized view — no WindowSizeMsg yet, as in a test — is fitting nothing.
	if m.height <= 0 {
		return block
	}

	// One row buys nothing but the notice that everything was dropped, so below
	// two the block goes entirely and View falls back to its footer: a help
	// panel that cannot fit is not help.
	budget := m.helpBudget()
	if budget < 2 {
		return ""
	}

	return clampLines(block, budget)
}

// helpBudget is how many lines the help block may occupy on a sized view: at
// most half the terminal, and never more than what is left once the title, the
// blank line under it, one body line and the block's own leading blank line are
// accounted for. Half is the same deliberate cap the picker makes — a block
// that displaces the rows it is describing is a bad trade — and the fit term
// still governs small terminals, where it is the smaller of the two.
func (m detailModel) helpBudget() int {
	return min(m.height-4, m.height/2)
}
