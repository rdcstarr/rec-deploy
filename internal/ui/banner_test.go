package ui

import (
	"strings"
	"testing"
)

func TestBannerString(t *testing.T) {
	out := bannerString("v9.9.9")

	// The literal tagline, not the const, so a wrong-product banner is caught.
	for _, want := range []string{"deploy GitHub repos in place", "v9.9.9", "█"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing %q in:\n%s", want, out)
		}
	}

	// Six rows of art + a blank line + the tagline line.
	if lines := strings.Count(out, "\n") + 1; lines < 7 {
		t.Errorf("expected the multi-line art banner, got %d lines:\n%s", lines, out)
	}

	t.Logf("\n%s", out)
}

func TestCompactBannerString(t *testing.T) {
	out := compactBannerString("v9.9.9")
	for _, want := range []string{"rec-deploy", "deploy GitHub repos in place", "v9.9.9"} {
		if !strings.Contains(out, want) {
			t.Errorf("compact banner missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "█") {
		t.Errorf("compact banner contains wide art: %q", out)
	}
}
