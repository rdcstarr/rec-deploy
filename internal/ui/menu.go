package ui

import "errors"

// Menu is a reusable navigation level. Options and Help are functions so a
// screen can refresh dynamic state and command help on every redraw.
type Menu struct {
	Title      string
	Options    func() []Option
	Help       func() string
	SelectHelp string
	BackValues map[string]bool
	Handle     func(string) error
	// pick renders one choice. It defaults to the real Picker and exists only
	// so a test can drive the loop without a terminal.
	pick func(Picker) (Result, error)
}

// Run displays the menu until the operator backs out or quits. Child ErrBack
// returns to this menu, ErrQuit unwinds the session, and ordinary errors are
// rendered before the menu is shown again.
func (m Menu) Run() error {
	for {
		if Quitting() {
			return ErrQuit
		}
		var help string
		if m.Help != nil {
			help = m.Help()
		}
		var options []Option
		if m.Options != nil {
			options = m.Options()
		}
		pick := m.pick
		if pick == nil {
			pick = Picker.Run
		}
		res, err := pick(Picker{Title: m.Title, Options: options, Help: help, SelectHelp: m.SelectHelp})
		if err != nil {
			return err
		}
		if res.Value == "" || m.BackValues[res.Value] {
			return ErrBack
		}
		if m.Handle == nil {
			continue
		}
		err = m.Handle(res.Value)
		switch {
		case IsQuit(err):
			return err
		case errors.Is(err, ErrDone):
			// A dispatched command completed — unwind past this menu and every
			// one above it, so the operator lands back at the shell with the
			// command's output in view instead of the menu redrawn over it.
			return ErrDone
		case errors.Is(err, ErrBack):
			continue
		case err != nil:
			RenderError(err)
		}
		Out("")
	}
}
