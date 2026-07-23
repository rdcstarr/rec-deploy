package ui

import (
	"errors"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// ErrQuit signals that the user asked to quit the whole interactive session —
// q in navigation screens or Ctrl+C anywhere. It must propagate up through every
// interactive loop to the top, where Execute turns it into a clean exit. Test
// for it with IsQuit; never render it as an error.
var ErrQuit = errors.New("rec-deploy: quit interactive session")

// ErrBack signals that the user backed out of the current screen with Esc. It lets a
// caller tell "backed out" from a legitimate empty submission, so a prompt whose
// empty value drives an action (e.g. "empty = all paths" on a deploy) can skip
// that action. It is not a real error: RenderError ignores it and Execute treats
// it as a clean exit, so it harmlessly unwinds to the nearest menu loop.
var ErrBack = errors.New("rec-deploy: back one level")

// IsQuit reports whether err signals a full-session quit (ErrQuit).
func IsQuit(err error) bool { return errors.Is(err, ErrQuit) }

// quitRequested records a pending session quit. The interactive UI is
// single-threaded — each Picker/form runs to completion before control returns —
// so this package-level flag is race-free, and it lets every call site that
// ignores a prompt's result stay unchanged: only the menu loops consult
// Quitting(). A quit key sets it; the menu loops turn it into ErrQuit; ResetQuit
// clears it when a fresh command run begins.
var quitRequested bool

// Quitting reports whether a quit key was pressed in any menu or form. Menu loops
// check it at the top of each iteration and return ErrQuit so the whole session
// unwinds — even when the quit happened inside a nested form whose error the
// caller ignored.
func Quitting() bool { return quitRequested }

// ResetQuit clears the pending-quit flag. Call it at the start of a command run
// (PersistentPreRunE) so a quit never leaks across invocations.
func ResetQuit() { quitRequested = false }

// requestQuit marks that the user asked to quit the session.
func requestQuit() { quitRequested = true }

// nav is the outcome of an interactive form.
type nav int

const (
	navProceed nav = iota // the user submitted the form
	navBack               // the user stepped back one level (Esc)
	navQuit               // the user quit the session (Ctrl+C, or q on a screen)
)

type navigationContext int

const (
	navigationInput navigationContext = iota
	navigationMenu
	navigationDetail
)

// navigationKey is the single key contract shared by every TUI component.
// Option+arrows are intentionally absent: terminals use them for word movement
// and may encode them as an Esc-prefixed sequence that leaks into the next view.
func navigationKey(key string, context navigationContext) nav {
	if key == "ctrl+c" || (context != navigationInput && key == "q") {
		return navQuit
	}
	if key == "esc" || (context == navigationMenu && key == "left") || (context == navigationDetail && (key == "left" || key == "enter")) {
		return navBack
	}
	return navProceed
}

func navigationFooter(context navigationContext) string {
	if context == navigationInput {
		return "esc back • ctrl+c quit"
	}
	return "esc/← back • q/ctrl+c quit"
}

// formModel wraps a huh.Form so Esc and Ctrl+C have identical behavior in
// every input while all text-editing chords continue to reach the field.
type formModel struct {
	form     *huh.Form
	footer   string
	nav      nav
	secrets  map[string]bool
	revealed bool
}

// Init implements tea.Model.
func (m formModel) Init() tea.Cmd { return m.form.Init() }

// Update implements tea.Model: it claims the back and quit chords and otherwise
// delegates to the wrapped form, quitting once the form is no longer running.
func (m formModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		if matchesPickerKey(k.String(), "alt+r") {
			focused := m.form.GetFocusedField()
			if m.secrets[focused.GetKey()] {
				m.revealed = !m.revealed
				mode := huh.EchoModePassword
				if m.revealed {
					mode = huh.EchoModeNormal
				}
				focused.(*huh.Input).EchoMode(mode)
				model, cmd := m.form.Update(nil)
				if form, ok := model.(*huh.Form); ok {
					m.form = form
				}
				return m, cmd
			}
			return m, nil
		}
		switch navigationKey(k.String(), navigationInput) {
		case navBack:
			m.nav = navBack
			return m, tea.Quit
		case navQuit:
			m.nav = navQuit
			return m, tea.Quit
		}
	}

	model, cmd := m.form.Update(msg)
	if f, ok := model.(*huh.Form); ok {
		m.form = f
	}
	if m.form.State != huh.StateNormal {
		return m, tea.Quit
	}

	return m, cmd
}

// View implements tea.Model: the form plus a navigation hint footer so back/quit
// (and field movement) are discoverable, mirroring the picker's footer.
func (m formModel) View() string {
	if m.nav != navProceed {
		return ""
	}

	// A submitted huh form renders "" (its own quitting guard). Appending the
	// footer to that would make the final frame two rows — an empty one and the
	// footer — and bubbletea's renderer erases only the last row when a program
	// exits. The empty row survived, which is why every answered prompt used to
	// leave a blank line behind and the spacing looked arbitrary.
	view := m.form.View()
	if view == "" {
		return ""
	}
	if m.footer != "" {
		view += "\n" + m.footer
	}

	return view
}

// formFooter renders the navigation hint shown under a form, mirroring the
// picker's footer. multi (a form with more than one field) also advertises the
// ↑/↓ field movement that the shared keymap enables.
func formFooter(multi, reveal bool) string {
	hints := make([]string, 0, 4)
	if multi {
		hints = append(hints, "↑/↓ move")
	}
	if reveal {
		hints = append(hints, "⌥R reveal")
	}
	hints = append(hints, navigationFooter(navigationInput))

	return render(StyleSubtle, strings.Join(hints, " • "))
}

// formKeyMap is the shared form keymap: huh's default plus ↑/↓ on field
// navigation. huh moves between fields with tab/shift+tab only, so without this a
// multi-field form (ui.Form) cannot be traversed with the arrow keys. On a
// single-field form both bindings auto-disable (the field is first and last), so
// adding them changes nothing there.
func formKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Input.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"), key.WithHelp("↑", "back"))
	km.Input.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"), key.WithHelp("↓", "next"))

	return km
}

// runForm runs the given fields as one group with the shared theme and the
// back/quit key interception, returning the navigation outcome. It mirrors huh's
// own wrapping (group + form, help hidden). A pending quit short-circuits so
// nothing renders once the session is already unwinding.
func runForm(fields ...huh.Field) nav {
	return runFormWithSecrets(fields, nil)
}

// runFormWithSecrets runs a form whose listed inputs can be revealed in place
// with Alt+R. Secrets start masked and are masked again whenever a fresh form
// opens; their clear text never appears outside that editor.
func runFormWithSecrets(fields []huh.Field, secrets map[string]bool) nav {
	if Quitting() {
		return navQuit
	}

	form := huh.NewForm(huh.NewGroup(fields...)).
		WithTheme(huh.ThemeCharm()).
		WithShowHelp(false).
		WithKeyMap(formKeyMap())

	// Match huh's own program options so a wrapped form renders exactly where a
	// bare huh form would — on stderr, with focus reporting.
	res, err := tea.NewProgram(formModel{
		form: form, footer: formFooter(len(fields) > 1, len(secrets) > 0), secrets: secrets,
	},
		tea.WithOutput(os.Stderr),
		tea.WithReportFocus(),
	).Run()
	if err != nil {
		requestQuit()
		return navQuit
	}

	n := res.(formModel).nav
	if n == navQuit {
		requestQuit()
	}

	return n
}
