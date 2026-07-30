package ui

import (
	"bytes"
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

// TestOutDownsamplesToTheTerminalProfile pins that styled output goes through
// lipgloss's writer, which strips or reduces colour the terminal cannot show.
// lipgloss v2 downsamples at write time, not in Render, so a plain
// fmt.Fprintln would leak truecolor sequences onto a 16-colour console.
func TestOutDownsamplesToTheTerminalProfile(t *testing.T) {
	SetColor(true)
	defer SetColor(false)

	var buf bytes.Buffer
	if _, err := lipgloss.Fprintln(&buf, render(StyleTitle, "title")); err != nil {
		t.Fatalf("Fprintln: %v", err)
	}
	if strings.Contains(buf.String(), "\x1b[38;2;") {
		t.Errorf("truecolor sequence survived downsampling to a non-tty: %q", buf.String())
	}
}
