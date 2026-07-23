package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestQuitFlag(t *testing.T) {
	ResetQuit()
	if Quitting() {
		t.Fatal("expected not quitting after ResetQuit")
	}

	requestQuit()
	if !Quitting() {
		t.Fatal("expected quitting after requestQuit")
	}

	ResetQuit()
	if Quitting() {
		t.Fatal("expected not quitting after a second ResetQuit")
	}
}

func TestIsQuit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"ErrQuit", ErrQuit, true},
		{"wrapped ErrQuit", fmt.Errorf("dispatch: %w", ErrQuit), true},
		{"ErrBack", ErrBack, false},
		{"nil", nil, false},
		{"other", errors.New("boom"), false},
	}

	for _, c := range cases {
		if got := IsQuit(c.err); got != c.want {
			t.Errorf("IsQuit(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNavigationKeyContract(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		context navigationContext
		want    nav
	}{
		{name: "input escape backs out", key: "esc", context: navigationInput, want: navBack},
		{name: "input ctrl-c quits", key: "ctrl+c", context: navigationInput, want: navQuit},
		{name: "input q remains text", key: "q", context: navigationInput, want: navProceed},
		{name: "input option-left remains editing", key: "alt+left", context: navigationInput, want: navProceed},
		{name: "terminal option-left remains editing", key: "alt+b", context: navigationInput, want: navProceed},
		{name: "menu escape backs out", key: "esc", context: navigationMenu, want: navBack},
		{name: "menu left backs out", key: "left", context: navigationMenu, want: navBack},
		{name: "menu q quits", key: "q", context: navigationMenu, want: navQuit},
		{name: "detail enter backs out", key: "enter", context: navigationDetail, want: navBack},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := navigationKey(test.key, test.context); got != test.want {
				t.Errorf("navigationKey(%q, %d) = %d, want %d", test.key, test.context, got, test.want)
			}
		})
	}
}

func TestRenderErrorIgnoresNavSignals(t *testing.T) {
	// The navigation signals are not real errors and must never be rendered.
	for _, err := range []error{nil, ErrQuit, ErrBack, fmt.Errorf("dispatch: %w", ErrQuit)} {
		if out := captureStderr(t, func() { RenderError(err) }); out != "" {
			t.Errorf("RenderError(%v) printed %q, want nothing", err, out)
		}
	}

	// A real error is still rendered.
	if out := captureStderr(t, func() { RenderError(errors.New("boom")) }); !strings.Contains(out, "boom") {
		t.Errorf("RenderError(real error) printed %q, want it to contain the message", out)
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what it
// wrote. RenderError writes straight to os.Stderr, so the test swaps it out.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()
	_ = w.Close()

	out, _ := io.ReadAll(bufio.NewReader(r))

	return string(out)
}
