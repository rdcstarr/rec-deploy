package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
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

// ErrDone signals that a dispatched command ran to completion, so the whole
// interactive session should unwind to the shell with that command's output in
// view rather than redraw the menu on top of it. It is the difference between
// "backed out, show me the menu again" (ErrBack) and "the thing I asked for is
// done" (ErrDone) — a repo install, a deploy, a rotate finishing should leave
// the operator at their prompt, not three menus deep. Menu loops propagate it;
// Execute treats it as a clean exit.
var ErrDone = errors.New("rec-deploy: request completed")

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

// frameEnd terminates the last line of a view that has sized itself to the
// terminal — with nothing, because a frame is measured by its newlines and the
// one after the footer opens a further row. A view that has not been sized (no
// WindowSizeMsg yet, as in a test) is fitting nothing and keeps the newline, so
// its output still reads as a finished block of text.
//
// The extra row is not cosmetic under Bubble Tea v2. Rendering inline, it drops
// the top of an oversized frame — which silently ate the title of every screen
// long enough to scroll — and its "unchanged frame, nothing to write" check
// compares the frame against a screen buffer it has already truncated to the
// terminal height. The two can never match again, so the renderer repainted a
// byte-identical screen at full framerate with no input at all: 181 full-screen
// clears and 215KB in three idle seconds on `rec-deploy logs`.
func frameEnd(height int) string {
	if height <= 0 {
		return "\n"
	}

	return ""
}

// clampFrame truncates an assembled frame to its last height rows, so no view
// in this package can hand Bubble Tea v2 more rows than the terminal has —
// whatever its own arithmetic did.
//
// The arithmetic is what keeps a frame inside the terminal, and it still does
// the work: this finds nothing to do whenever a view fitted itself properly. It
// exists because the failure mode of getting that arithmetic wrong is not a
// cosmetic one. v2's renderer gates its "unchanged frame, nothing to write"
// check on the frame area matching a screen buffer it has already truncated to
// the terminal height; once a frame overflows, the two can never match again
// and it repaints a byte-identical screen at full framerate with no input at
// all — a CPU spin on an idle terminal. Below five rows no view here can fit a
// title, a row of content and a footer at once, so there the overflow is not a
// mistake to be corrected but a geometry to be cropped.
//
// It keeps the last rows rather than the first, which is what Bubble Tea v1 did
// with an oversized frame (standard_renderer.go: newLines[len(newLines)-r.height:]).
// The footer carries the keys the operator needs to leave the screen, so the
// title is what goes when only one of them can stay.
//
// It wraps frameEnd, never the other way round: frameEnd decides whether a
// frame's last line opens a further row, so clamping first would let it push
// the frame back over the height by one.
func clampFrame(content string, height int) string {
	if height <= 0 {
		return content
	}

	lines := strings.Split(content, "\n")
	if len(lines) <= height {
		return content
	}

	return strings.Join(lines[len(lines)-height:], "\n")
}

// clampLines truncates a pre-rendered block to at most limit lines, keeping the
// trailing newline the callers build their frames around. It is the other half
// of frameEnd: windowing the content a view owns cannot bring an oversized
// frame back inside the terminal when the block *below* that content is itself
// taller than the terminal — the list bottoms out at one row and the frame
// still overflows. cmd/help.go hands a whole command's help to a menu or a
// detail view, and root's runs to 24 lines, so on the 24-row terminal that is
// the common case the block has to be bounded in its own right.
//
// It clamps rather than scrolls: the block is reference material toggled on
// with h, not the screen's subject, so giving it a second independent scroll
// offset would fork what the arrow keys mean depending on a toggle — and the
// navigation contract is one table shared by every view. The last kept line
// says how many were dropped, so a cut panel reads as cut, and the full text is
// always a `--help` away.
//
// Callers never pass a limit below two — one row buys nothing but the notice
// that everything was dropped, so they drop the block instead — but the guard
// keeps the helper total rather than panicking on a slice bound.
func clampLines(block string, limit int) string {
	if limit <= 0 {
		return block
	}

	lines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	if len(lines) <= limit {
		return block
	}

	dropped := len(lines) - limit + 1
	lines = lines[:limit]
	lines[limit-1] = render(StyleSubtle, fmt.Sprintf("  … %d more lines", dropped))

	return strings.Join(lines, "\n") + "\n"
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

// Init implements tea.Model. It asks the terminal for its background color
// because huh's theme is resolved per render from that answer: without the
// query the form never receives a tea.BackgroundColorMsg, huh's hasDarkBg stays
// false and every terminal gets the light palette. huh's own Form.Init requests
// only the window size, so this has to be added here.
func (m formModel) Init() tea.Cmd {
	return tea.Batch(m.form.Init(), tea.RequestBackgroundColor)
}

// Update implements tea.Model: it claims the back and quit chords and otherwise
// delegates to the wrapped form, quitting once the form is no longer running.
func (m formModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
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
func (m formModel) View() tea.View {
	if m.nav != navProceed {
		return tea.NewView("")
	}

	// A submitted huh form renders "" (its own quitting guard). Appending the
	// footer to that would make the final frame two rows — an empty one and the
	// footer — and bubbletea's renderer erases only the last row when a program
	// exits. The empty row survived, which is why every answered prompt used to
	// leave a blank line behind and the spacing looked arbitrary.
	view := m.form.View()
	if view == "" {
		return tea.NewView("")
	}
	if m.footer != "" {
		view += "\n" + m.footer
	}

	// Focus reporting moved from a program option onto the view in bubbletea v2,
	// so it is set here to keep matching huh's own program options.
	v := tea.NewView(view)
	v.ReportFocus = true

	return v
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
		WithTheme(huh.ThemeFunc(huh.ThemeCharm)).
		WithShowHelp(false).
		WithKeyMap(formKeyMap())

	// Match huh's own program options so a wrapped form renders exactly where a
	// bare huh form would — on stderr. Focus reporting is a view field now, set
	// by formModel.View.
	res, err := tea.NewProgram(formModel{
		form: form, footer: formFooter(len(fields) > 1, len(secrets) > 0), secrets: secrets,
	},
		tea.WithOutput(os.Stderr),
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
