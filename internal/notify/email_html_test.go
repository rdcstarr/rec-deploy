package notify

import (
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/store"
)

// TestRenderHTMLEscapesPushControlledInput pins the security boundary: commit
// messages and step reasons come from whoever can push, and must never reach
// the HTML unescaped.
func TestRenderHTMLEscapesPushControlledInput(t *testing.T) {
	html, err := RenderHTML(Summary{
		Repository: "o/r",
		Ref:        "refs/heads/main",
		Status:     "failed",
		Message:    "<script>alert(1)</script>",
		Paths: []PathSummary{
			{Path: "/srv/x", User: "u", Status: "failed", Reason: `<b onmouseover="x">boom</b>`},
		},
	})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if strings.Contains(html, "<script>") || strings.Contains(html, "<b ") {
		t.Errorf("push-controlled markup survived escaping:\n%s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("escaped message missing from output")
	}
}

// TestRenderHTMLSuccessBody checks the success card carries the CLI story: the
// push line, the amber sha, each installation with its glyph, the root badge on
// exactly the root path, and the green verdict — with no failure artifacts.
func TestRenderHTMLSuccessBody(t *testing.T) {
	html, err := RenderHTML(Summary{
		Repository: "rdcstarr/rec-deploy-test",
		Ref:        "refs/heads/main",
		SHA:        "759daa4deadbeef",
		Author:     "rdcstarr",
		Message:    "test: v3 — trigger a webhook deploy\nsecond line ignored",
		Status:     "success",
		Paths: []PathSummary{
			{Path: "/var/www/rec-test", User: "www-data", Status: "success"},
			{Path: "/opt/rec-test", Status: "success", RanAsRoot: true},
		},
	})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}

	for _, want := range []string{
		"$ push 759daa4 → rdcstarr/rec-deploy-test@main",
		">759daa4</span> by rdcstarr",
		"test: v3 — trigger a webhook deploy",
		"/var/www/rec-test",
		"(www-data)",
		"⚠ root",
		"✓ deployed",
		"2 installations",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("success card missing %q", want)
		}
	}
	if strings.Count(html, "⚠ root") != 1 {
		t.Errorf("root badge should mark exactly one path")
	}
	for _, forbid := range []string{"second line ignored", "journalctl", "└"} {
		if strings.Contains(html, forbid) {
			t.Errorf("success card unexpectedly contains %q", forbid)
		}
	}
}

// TestRenderHTMLFailureBody checks the failed card: the red verdict, the
// journalctl hint, the reason continuation line, and the failure count.
func TestRenderHTMLFailureBody(t *testing.T) {
	html, err := RenderHTML(Summary{
		Repository: "o/r",
		Ref:        "refs/heads/main",
		Status:     "failed",
		Error:      "one installation failed",
		Paths: []PathSummary{
			{Path: "/a", User: "u", Status: "success"},
			{Path: "/b", User: "v", Status: store.StatusRolledBack, Reason: "step `x` timed out after 10m"},
		},
	})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	for _, want := range []string{
		"! failed",
		"journalctl -u rec-deploy",
		"└ step `x` timed out after 10m",
		"rolled_back",
		"1 of 2 installations failed",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("failure card missing %q", want)
		}
	}
	if strings.Contains(html, "✓ deployed") {
		t.Errorf("failure card claims success")
	}
}

// TestRenderHTMLNeutralStatuses checks that outcomes which are neither success
// nor failure — a skipped deploy (branch filter) or the try-it notification
// from `rec-deploy init` — get the amber "›" verdict and never borrow the
// failure card's red livery or its journalctl hint.
func TestRenderHTMLNeutralStatuses(t *testing.T) {
	html, err := RenderHTML(Summary{
		Repository: "o/r",
		Ref:        "refs/heads/main",
		Status:     "test",
	})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	for _, forbid := range []string{"journalctl", "! test", "#3a2724"} {
		if strings.Contains(html, forbid) {
			t.Errorf("test-status card unexpectedly contains %q", forbid)
		}
	}
	for _, want := range []string{"› test", "#23272c"} {
		if !strings.Contains(html, want) {
			t.Errorf("test-status card missing %q", want)
		}
	}

	html, err = RenderHTML(Summary{
		Repository: "o/r",
		Ref:        "refs/heads/main",
		Status:     store.StatusSkipped,
		Paths: []PathSummary{
			{Path: "/a", User: "u", Status: store.StatusSkipped},
		},
	})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if !strings.Contains(html, `color:#e0a63f">skipped</span>`) {
		t.Errorf("skipped card missing amber status word")
	}
	if strings.Contains(html, "#3a2724") {
		t.Errorf("skipped card unexpectedly wears the failure border")
	}
}
