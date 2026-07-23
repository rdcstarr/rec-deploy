package ui

import (
	"fmt"
	"os"
	"time"

	"github.com/mattn/go-isatty"
)

// spinnerFrames is the braille dot cycle shown while an action runs.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner runs action while animating a titled spinner on stderr, returning
// action's error. Without an interactive terminal (piped, CI) it just runs
// action, so callers never emit escape codes into captured output. Use it to
// wrap any blocking work — a network lookup, an external command — so the user
// sees progress instead of a dead pause.
func Spinner(title string, action func() error) error {
	if !isatty.IsTerminal(os.Stderr.Fd()) {
		return action()
	}

	done := make(chan error, 1)
	go func() { done <- action() }()

	tick := time.NewTicker(90 * time.Millisecond)
	defer tick.Stop()

	for i := 0; ; i++ {
		select {
		case err := <-done:
			fmt.Fprint(os.Stderr, "\r\x1b[2K") // clear the spinner line
			return err
		case <-tick.C:
			fmt.Fprintf(os.Stderr, "\r\x1b[2K%s %s",
				render(StyleHighlight, spinnerFrames[i%len(spinnerFrames)]),
				render(StyleSubtle, title))
		}
	}
}
