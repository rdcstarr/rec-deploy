package ui

import (
	"fmt"

	"github.com/charmbracelet/huh"
)

// Option is a selectable choice with a display label and an underlying value.
type Option struct {
	Label string
	Value string
}

// Select shows an interactive single-choice list and returns the chosen value
// ("" when the user backs out). It is backed by Picker so every menu gets the
// same keybindings, including h for help. It requires an interactive terminal.
func Select(title string, options []Option) (string, error) {
	res, err := Picker{Title: title, Options: options}.Run()

	return res.Value, err
}

// newInput builds a text input with the shared title and inline-help wiring.
// An empty desc renders nothing.
func newInput(title, desc string) *huh.Input {
	return huh.NewInput().Title(title).Description(desc)
}

// MultiSelect shows an interactive multi-choice list and returns the chosen
// values. desc renders as persistent inline help under the title; empty shows
// nothing. Esc backs out; Ctrl+C quits. It requires an interactive terminal.
func MultiSelect(title, desc string, options []Option) ([]string, error) {
	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o.Label, o.Value)
	}

	var values []string

	switch runForm(huh.NewMultiSelect[string]().
		Title(title).
		Description(desc).
		Options(opts...).
		Value(&values)) {
	case navQuit:
		return nil, ErrQuit
	case navBack:
		return nil, ErrBack
	default:
		return values, nil
	}
}

// MultiSelectDefault is MultiSelect with the options whose value is in selected
// pre-checked, for toggle-and-apply menus. desc renders as persistent inline
// help under the title; empty shows nothing. Backing out or quitting leaves the
// selection unchanged (quitting also returns ErrQuit). It requires an
// interactive terminal.
func MultiSelectDefault(title, desc string, options []Option, selected []string) ([]string, error) {
	on := make(map[string]bool, len(selected))
	for _, s := range selected {
		on[s] = true
	}

	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o.Label, o.Value).Selected(on[o.Value])
	}

	values := append([]string(nil), selected...)

	switch runForm(huh.NewMultiSelect[string]().
		Title(title).
		Description(desc).
		Options(opts...).
		Value(&values)) {
	case navQuit:
		return selected, ErrQuit
	case navBack:
		return selected, ErrBack
	default:
		return values, nil
	}
}

// Prompt asks for a single line of text, pre-filled with defaultValue. desc
// renders as persistent inline help under the title; empty shows nothing.
// Backing out with Esc returns ""; quitting with Ctrl+C returns
// ErrQuit. It requires an interactive terminal.
func Prompt(title, desc, defaultValue string) (string, error) {
	value := defaultValue

	switch runForm(newInput(title, desc).Value(&value)) {
	case navQuit:
		return "", ErrQuit
	case navBack:
		return "", ErrBack
	default:
		return value, nil
	}
}

// Field is one labeled text input in a Form, bound to Value: the input is
// pre-filled with *Value and *Value is overwritten with the submitted text.
// Desc renders as persistent inline help under the title; empty shows nothing.
type Field struct {
	Title string
	Desc  string
	// Secret masks the input; the typed value never renders.
	Secret bool
	Value  *string
}

// buildInputs turns the field descriptions into huh inputs — split out of Form
// so the Secret wiring is testable without an interactive terminal.
func buildInputs(fields []Field) ([]huh.Field, map[string]bool) {
	inputs := make([]huh.Field, len(fields))
	secrets := make(map[string]bool)
	for i, f := range fields {
		in := newInput(f.Title, f.Desc).Value(f.Value)
		if f.Secret {
			key := fmt.Sprintf("rec-deploy-secret-%d", i)
			in = in.Key(key).EchoMode(huh.EchoModePassword)
			secrets[key] = true
		}
		inputs[i] = in
	}

	return inputs, secrets
}

// Form shows every field as a text input in a single form, pre-filled from its
// bound Value and written back on submit, so the user edits only the ones they
// want. Backing out with Esc returns ErrBack and leaves the Values
// untouched; quitting with Ctrl+C returns ErrQuit. It requires an interactive
// terminal.
func Form(fields []Field) error {
	inputs, secrets := buildInputs(fields)
	switch runFormWithSecrets(inputs, secrets) {
	case navQuit:
		return ErrQuit
	case navBack:
		return ErrBack
	default:
		return nil
	}
}

// Password asks for a single line of secret text with masked input. Alt+R
// reveals or masks the value in place. desc
// renders as persistent inline help under the title; empty shows nothing.
// Backing out returns ""; quitting returns ErrQuit. It requires an interactive
// terminal.
func Password(title, desc string) (string, error) {
	return credentialPrompt(title, desc, "")
}

// SecretPrompt edits an existing secret in a masked input. Alt+R reveals or
// masks the complete value in place, allowing it to be inspected and edited.
func SecretPrompt(title, desc, currentValue string) (string, error) {
	return credentialPrompt(title, desc, currentValue)
}

func credentialPrompt(title, desc, currentValue string) (string, error) {
	value := currentValue
	const key = "rec-deploy-secret"
	input := newInput(title, desc).Key(key).EchoMode(huh.EchoModePassword).Value(&value)

	switch runFormWithSecrets([]huh.Field{input}, map[string]bool{key: true}) {
	case navQuit:
		return "", ErrQuit
	case navBack:
		return "", ErrBack
	default:
		return value, nil
	}
}

// Confirm asks a yes/no question. desc renders as persistent inline help under
// the title; empty shows nothing. Esc backs out; Ctrl+C quits the session.
func Confirm(title, desc string) (bool, error) {
	var value bool

	switch runForm(huh.NewConfirm().
		Title(title).
		Description(desc).
		Value(&value)) {
	case navQuit:
		return false, ErrQuit
	case navBack:
		return false, ErrBack
	default:
		return value, nil
	}
}
