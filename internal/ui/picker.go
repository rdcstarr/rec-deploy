package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Action defines a key that acts on the highlighted item and returns the new
// (possibly reordered) option list, without closing the list. The picker keeps
// the acted item focused after the update — e.g. toggling a favorite re-sorts it
// to the top and the cursor follows it there.
type Action struct {
	Key  string
	Help string
	Run  func(value string) (options []Option, err error)
}

// Stats defines a key that toggles an extra per-row detail (e.g. usage
// statistics) for every item, computed on demand by Of.
type Stats struct {
	Key     string
	Help    string
	Of      func(value string) string
	Timeout time.Duration
}

// Key is a picker key that selects the highlighted item and exits, reporting
// which key was pressed so the caller can branch on it (e.g. e=edit, d=delete).
type Key struct {
	Key  string
	Help string
}

// Result is what Picker.Run reports: the highlighted value at exit and the key
// that caused the exit — "enter" when chosen, an exit Key (e.g. "e"), or "" when
// the user went back.
type Result struct {
	Value string
	Key   string
}

// Picker is an interactive single-choice list. Beyond moving the cursor and
// choosing with Enter, it supports an optional Action that relabels the
// highlighted item and an optional Stats toggle that reveals a per-row detail.
type Picker struct {
	Title      string
	Options    []Option
	Action     *Action
	Stats      *Stats
	SelectHelp string
	// Keys are extra keys that select the highlighted item and exit, each
	// reported back via Result.Key so the caller can branch (e.g. e=edit).
	Keys []Key
	// Help, when set, is pre-rendered help shown when help is toggled with h
	// (e.g. a command's commands/flags); otherwise h shows the keybindings.
	Help string
}

// Run shows the picker and returns the highlighted value plus the key that
// exited: "enter" when chosen, an exit Key (e.g. "e"), or "" if the user went
// back (Esc or ←). It returns ErrQuit if the user quits with q or Ctrl+C.
func (p Picker) Run() (Result, error) {
	if Quitting() {
		return Result{}, ErrQuit
	}

	res, err := tea.NewProgram(pickerModel{Picker: p}).Run()
	if err != nil {
		return Result{}, err
	}

	final := res.(pickerModel)
	if final.quit {
		requestQuit()
		return Result{}, ErrQuit
	}

	return Result{Value: final.chosen, Key: final.key}, final.err
}

// pickerModel is the bubbletea model backing Picker.
type pickerModel struct {
	Picker
	cursor          int
	showStats       bool
	statsGeneration int
	showHelp        bool
	chosen          string
	key             string
	err             error
	quitting        bool
	quit            bool
	width           int
}

// Init implements tea.Model.
func (m pickerModel) Init() tea.Cmd { return nil }

// Update implements tea.Model: up/down move the cursor, Enter chooses,
// Esc / ← go back one level (return ""), q / Ctrl+C quit the
// whole session, the action key acts on the highlighted item, and the stats key
// toggles the per-row detail.
func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = size.Width
		return m, nil
	}
	if timeout, ok := msg.(statsTimeoutMsg); ok {
		if timeout.generation == m.statsGeneration {
			m.showStats = false
		}
		return m, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	s := key.String()

	switch {
	case navigationKey(s, navigationMenu) == navQuit:
		m.quit = true
		m.quitting = true
		return m, tea.Quit

	case navigationKey(s, navigationMenu) == navBack:
		m.quitting = true
		return m, tea.Quit

	case s == "up" || s == "k":
		if m.cursor > 0 {
			m.cursor--
		}

	case s == "down" || s == "j":
		if m.cursor < len(m.Options)-1 {
			m.cursor++
		}

	case s == "enter":
		if len(m.Options) > 0 {
			m.chosen = m.Options[m.cursor].Value
			m.key = "enter"
		}
		m.quitting = true
		return m, tea.Quit

	case len(m.Options) > 0 && m.exitKey(s):
		m.chosen = m.Options[m.cursor].Value
		m.key = s
		m.quitting = true
		return m, tea.Quit

	case m.Action != nil && s == m.Action.Key:
		if m.Action.Run == nil || len(m.Options) == 0 {
			return m, nil
		}
		acted := m.Options[m.cursor].Value
		options, err := m.Action.Run(acted)
		if err != nil {
			m.err = err
			m.quitting = true
			return m, tea.Quit
		}
		if options != nil {
			m.Options = options
			m.cursor = focusIndex(options, acted, m.cursor)
		}

	case m.Stats != nil && matchesPickerKey(s, m.Stats.Key):
		m.showStats = !m.showStats
		m.statsGeneration++
		if m.showStats && m.Stats.Timeout > 0 {
			generation := m.statsGeneration
			return m, tea.Tick(m.Stats.Timeout, func(time.Time) tea.Msg {
				return statsTimeoutMsg{generation: generation}
			})
		}

	case s == "h":
		m.showHelp = !m.showHelp
	}

	return m, nil
}

type statsTimeoutMsg struct {
	generation int
}

// matchesPickerKey accepts Bubble Tea's Alt chord and the character macOS
// terminals emit for Option+R when Option is not configured as Meta. A plain r
// never matches the sensitive reveal chord.
func matchesPickerKey(got, configured string) bool {
	return got == configured || (configured == "alt+r" && got == "®")
}

// View implements tea.Model.
func (m pickerModel) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder
	b.WriteString(render(StyleTitle, m.Title) + "\n\n")

	for i, o := range m.Options {
		marker, label := "  ", o.Label
		if m.width > 4 {
			label = ansi.Truncate(label, m.width-4, "…")
		}
		if i == m.cursor {
			marker = render(StyleHighlight, "▸ ")
			label = render(StyleHighlight, label)
		}

		row := marker + label
		if m.showStats && m.Stats != nil && m.Stats.Of != nil {
			if d := m.Stats.Of(o.Value); d != "" {
				if m.width > 0 {
					remaining := m.width - ansi.StringWidth(label) - 6
					if remaining < 1 {
						d = ""
					} else {
						d = ansi.Truncate(d, remaining, "…")
					}
				}
				if d != "" {
					row += "  " + render(StyleSubtle, d)
				}
			}
		}

		b.WriteString(row + "\n")
	}

	if m.showHelp {
		if m.Help != "" {
			b.WriteString("\n" + m.Help + "\n")
		} else {
			b.WriteString("\n" + HelpPanel("Keys", m.helpRows(), nil))
		}
	}

	b.WriteString("\n" + render(StyleSubtle, m.help()) + "\n")

	return b.String()
}

// exitKey reports whether s is one of the picker's exit Keys.
func (m pickerModel) exitKey(s string) bool {
	for _, k := range m.Keys {
		if k.Key == s {
			return true
		}
	}

	return false
}

// focusIndex returns the index of value in options so the cursor can follow an
// acted item after a reorder, clamping to fallback (then to range) when absent.
func focusIndex(options []Option, value string, fallback int) int {
	for i, o := range options {
		if o.Value == value {
			return i
		}
	}

	if fallback >= len(options) {
		return len(options) - 1
	}
	if fallback < 0 {
		return 0
	}

	return fallback
}

// help builds the footer hint line from the keys the picker currently exposes.
func (m pickerModel) help() string {
	hints := []string{"↑/↓ move", "enter " + m.selectHelp()}
	if m.Action != nil {
		hints = append(hints, m.Action.Key+" "+m.Action.Help)
	}
	if m.Stats != nil {
		hints = append(hints, displayKey(m.Stats.Key)+" "+m.Stats.Help)
	}
	for _, k := range m.Keys {
		hints = append(hints, k.Key+" "+k.Help)
	}
	hints = append(hints, navigationFooter(navigationMenu), "h help")

	return strings.Join(hints, " • ")
}

// helpRows lists the picker's keybindings for the help panel.
func (m pickerModel) helpRows() [][2]string {
	rows := [][2]string{
		{"↑/↓ j/k", "move"},
		{"enter", m.selectHelp()},
	}
	if m.Action != nil {
		rows = append(rows, [2]string{m.Action.Key, m.Action.Help})
	}
	if m.Stats != nil {
		rows = append(rows, [2]string{displayKey(m.Stats.Key), m.Stats.Help})
	}
	for _, k := range m.Keys {
		rows = append(rows, [2]string{k.Key, k.Help})
	}
	rows = append(rows,
		[2]string{"esc / ←", "back one level"},
		[2]string{"q / ctrl+c", "quit"},
		[2]string{"h", "toggle help"},
	)

	return rows
}

func (m pickerModel) selectHelp() string {
	if m.SelectHelp != "" {
		return m.SelectHelp
	}

	return "select"
}

// displayKey renders terminal key names as compact operator-facing chords.
func displayKey(key string) string {
	if rest, ok := strings.CutPrefix(key, "alt+"); ok {
		return "⌥" + strings.ToUpper(rest)
	}

	return key
}
