package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestScreenPath(t *testing.T) {
	if got := ScreenPath("rec-deploy", "Config", "Email"); got != "rec-deploy / Config / Email" {
		t.Errorf("ScreenPath = %q", got)
	}
}

func TestTwoColWidthWrapsLongRows(t *testing.T) {
	SetColor(false)
	t.Cleanup(func() { SetColor(true) })

	out := TwoColWidth([][2]string{{
		"/a/very/long/deployment/path",
		"a long explanation that should wrap instead of overflowing a narrow terminal",
	}}, 40)
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if width := lipgloss.Width(line); width > 40 {
			t.Errorf("line width = %d, want <= 40: %q", width, line)
		}
	}
	if !strings.Contains(out, "\n") {
		t.Errorf("long row did not wrap: %q", out)
	}
}

// TestOutDownsamplesToTheTerminalProfile pins that Out itself writes through
// lipgloss's writer, which downsamples colour to what the declared terminal
// profile can show. lipgloss v2 moved that reduction to write time, out of
// Style.Render, so a plain fmt.Fprintln inside Out would leak Style.Render's
// raw ANSI256 escape verbatim onto a terminal that only understands the
// basic 16-colour (ANSI) palette.
//
// CLICOLOR_FORCE and TERM are set so colorprofile.Detect settles on a stable
// ANSI (16-colour, not TrueColor) profile for the captured pipe: without
// CLICOLOR_FORCE, Detect treats any non-terminal file — including the pipe
// captureStdout swaps in for os.Stdout — as NoTTY and strips colour
// entirely, which would make this test pass whether or not Out actually
// downsamples, exactly like the ANSI256-index StyleTitle color never
// producing the truecolor (`38;2;`) prefix an earlier version of this test
// checked for.
func TestOutDownsamplesToTheTerminalProfile(t *testing.T) {
	SetColor(true)
	t.Cleanup(func() { SetColor(true) })
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("TERM", "xterm")

	raw := render(StyleTitle, "title")
	if !strings.Contains(raw, "38;5;") {
		t.Fatalf("StyleTitle no longer renders an ANSI256 escape to downsample from: %q", raw)
	}

	got := captureStdout(t, func() { Out(raw) })

	if strings.Contains(got, "38;5;") {
		t.Errorf("raw ANSI256 escape survived downsampling to a 16-colour profile: %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("colour was stripped entirely instead of downsampled to the terminal profile: %q", got)
	}
}
