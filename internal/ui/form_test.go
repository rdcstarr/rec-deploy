package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// TestFormKeymapAddsArrowNavigation checks that the shared keymap binds ↑/↓ to
// field navigation, the binding huh's default lacks. huh's Input field drives
// field movement off keymap.Input.Next/Prev, so this binding is what lets a
// multi-field form (ui.Form) be traversed with the arrows.
func TestFormKeymapAddsArrowNavigation(t *testing.T) {
	km := formKeyMap()

	if !key.Matches(tea.KeyMsg{Type: tea.KeyDown}, km.Input.Next) {
		t.Error("down should move to the next field")
	}
	if !key.Matches(tea.KeyMsg{Type: tea.KeyUp}, km.Input.Prev) {
		t.Error("up should move to the previous field")
	}
}

// TestFormFooterShowsBackAndQuit checks that a form renders the navigation hint
// (so "how do I go back?" is discoverable, like the picker's footer). A
// multi-field form also advertises ↑/↓ movement.
func TestFormFooterShowsBackAndQuit(t *testing.T) {
	multi := formFooter(true, false)
	for _, want := range []string{"back", "quit", "move"} {
		if !strings.Contains(multi, want) {
			t.Errorf("multi-field footer missing %q: %q", want, multi)
		}
	}

	single := formFooter(false, false)
	if strings.Contains(single, "move") {
		t.Errorf("single-field footer should not mention move: %q", single)
	}
	if !strings.Contains(single, "back") || !strings.Contains(single, "quit") {
		t.Errorf("single-field footer missing back/quit: %q", single)
	}

	var v string
	f := huh.NewForm(huh.NewGroup(huh.NewInput().Value(&v))).WithKeyMap(formKeyMap())
	f.Init()
	view := formModel{form: f, footer: formFooter(false, false)}.View()
	if !strings.Contains(view, "back") {
		t.Errorf("form view does not show the back hint:\n%s", view)
	}
}

// TestCompletedFormRendersNothing is the whole reason interactive output used to
// be littered with blank lines. huh renders "" once a form is submitted, so
// appending the footer to it left a two-row final frame — and bubbletea's
// renderer erases only the last row on exit, leaving the empty first row on
// screen after every prompt.
func TestCompletedFormRendersNothing(t *testing.T) {
	var v string
	f := huh.NewForm(huh.NewGroup(huh.NewInput().Value(&v))).WithKeyMap(formKeyMap())
	f.Init()

	model := formModel{form: f, footer: formFooter(false, false)}
	if model.View() == "" {
		t.Fatal("a live form rendered nothing; the test cannot tell completion apart")
	}

	// Enter submits the single-field form, which is what sets huh's own quitting
	// guard and makes Form.View() empty.
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = pump(t, next.(formModel), cmd)

	if f.State == huh.StateNormal {
		t.Fatalf("form did not complete on enter, state = %v", f.State)
	}
	if view := model.View(); view != "" {
		t.Errorf("completed form rendered %q, want an empty frame so nothing is left behind", view)
	}
}

// pump runs the commands a form returns and feeds each resulting message back
// in, until it stops producing any. huh completes a form over a message round
// trip rather than inside the key handler, so a test without a running
// bubbletea program has to drive that round trip itself.
func pump(t *testing.T, m formModel, cmd tea.Cmd) formModel {
	t.Helper()

	for i := 0; cmd != nil && i < 20; i++ {
		msgs := []tea.Msg{cmd()}
		if batch, ok := msgs[0].(tea.BatchMsg); ok {
			msgs = msgs[:0]
			for _, c := range batch {
				if c != nil {
					msgs = append(msgs, c())
				}
			}
		}

		cmd = nil
		for _, msg := range msgs {
			next, next2 := m.Update(msg)
			m = next.(formModel)
			if next2 != nil {
				cmd = next2
			}
		}
	}

	return m
}

// TestSecretFieldIsMasked pins the in-form password mask: the typed value must
// never appear in the rendered view.
func TestSecretFieldIsMasked(t *testing.T) {
	v := "hunter2secret"
	f := huh.NewForm(huh.NewGroup(newInput("Password", "").EchoMode(huh.EchoModePassword).Value(&v))).
		WithKeyMap(formKeyMap())
	f.Init()
	if view := (formModel{form: f, footer: formFooter(false, false)}).View(); strings.Contains(view, "hunter2secret") {
		t.Errorf("secret value visible in view:\n%s", view)
	}

	fields := []Field{{Title: "Password", Secret: true, Value: &v}}
	in, secrets := buildInputs(fields)
	if len(in) != 1 || len(secrets) != 1 {
		t.Fatalf("buildInputs = %d inputs", len(in))
	}

	built := huh.NewForm(huh.NewGroup(in...)).WithKeyMap(formKeyMap())
	built.Init()
	if view := (formModel{form: built, footer: formFooter(false, false)}).View(); strings.Contains(view, "hunter2secret") {
		t.Errorf("Field.Secret wiring in buildInputs did not mask the value:\n%s", view)
	}
}

// TestSecretFieldRevealTogglesInPlace checks that Alt+R reveals the current
// input value and a second press masks it again without changing that value.
func TestSecretFieldRevealTogglesInPlace(t *testing.T) {
	value := "stored-secret"
	const secretKey = "secret"
	input := newInput("Password", "").Key(secretKey).EchoMode(huh.EchoModePassword).Value(&value)
	form := huh.NewForm(huh.NewGroup(input)).WithKeyMap(formKeyMap())
	form.Init()
	model := formModel{form: form, footer: formFooter(false, true), secrets: map[string]bool{secretKey: true}}

	if view := model.View(); strings.Contains(view, value) {
		t.Fatalf("secret starts revealed:\n%s", view)
	}
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r"), Alt: true})
	model = next.(formModel)
	if view := model.View(); !strings.Contains(view, value) {
		t.Errorf("Alt+R did not reveal the input:\n%s", view)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r"), Alt: true})
	model = next.(formModel)
	if view := model.View(); strings.Contains(view, value) {
		t.Errorf("second Alt+R did not mask the input:\n%s", view)
	}
}

// TestDescriptionIsRendered checks that the desc argument reaches the rendered
// form — the inline help the wizard relies on — and that an empty desc adds
// nothing.
func TestDescriptionIsRendered(t *testing.T) {
	f := huh.NewForm(huh.NewGroup(newInput("Title", "find-me-desc").Value(new(string)))).
		WithKeyMap(formKeyMap())
	f.Init()
	if view := (formModel{form: f, footer: formFooter(false, false)}).View(); !strings.Contains(view, "find-me-desc") {
		t.Errorf("description text missing from view:\n%s", view)
	}

	bare := huh.NewForm(huh.NewGroup(newInput("Title", "").Value(new(string)))).
		WithKeyMap(formKeyMap())
	bare.Init()
	if view := (formModel{form: bare, footer: formFooter(false, false)}).View(); strings.Contains(view, "find-me-desc") {
		t.Errorf("unexpected description in bare view:\n%s", view)
	}
}
