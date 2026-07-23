package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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
