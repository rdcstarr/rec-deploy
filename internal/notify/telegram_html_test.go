package notify

import (
	"os"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/buildinfo"
)

// TestRenderTelegramHTMLEscapesPushControlledInput pins the security
// boundary: message, reason, author and path come from whoever can push, and
// must never reach the Bot API's HTML parser unescaped — including inside
// the <pre> code block, which Telegram still HTML-parses. The card's own
// tags must still render.
func TestRenderTelegramHTMLEscapesPushControlledInput(t *testing.T) {
	html := RenderTelegramHTML(Summary{
		Repository: "o/r",
		Ref:        "refs/heads/main",
		Status:     "failed",
		Message:    "<script>x</script>",
		Author:     "a<i>b",
		Paths: []PathSummary{
			{Path: "/srv/<b>x</b>", User: "u", Status: "failed", Reason: "<script>evil</script>"},
		},
	})

	for _, raw := range []string{"<script>x</script>", "a<i>b", "/srv/<b>x</b>", "<script>evil</script>"} {
		if strings.Contains(html, raw) {
			t.Errorf("push-controlled value %q survived unescaped:\n%s", raw, html)
		}
	}
	for _, want := range []string{"&lt;script&gt;x&lt;/script&gt;", "&lt;b&gt;x&lt;/b&gt;", "&lt;script&gt;evil&lt;/script&gt;"} {
		if !strings.Contains(html, want) {
			t.Errorf("escaped value missing from output:\n%s", html)
		}
	}
	for _, want := range []string{"<b>", "<code>", "<i>", "<pre>"} {
		if !strings.Contains(html, want) {
			t.Errorf("card's own tag %q missing:\n%s", want, html)
		}
	}
}

// TestRenderTelegramHTMLContent checks the success card carries the CLI
// story: the bold verdict, repo@branch in code, the server hostname, the
// commit sha, the root badge on exactly the root path, the paths inside a
// single <pre> block with no nested tags, and the version footer.
func TestRenderTelegramHTMLContent(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname: %v", err)
	}

	html := RenderTelegramHTML(Summary{
		Repository: "repo",
		Ref:        "refs/heads/main",
		SHA:        "759daa4deadbeef",
		Author:     "rdcstarr",
		Message:    "test: v3 — trigger a webhook deploy\nsecond line ignored",
		Status:     "success",
		Paths: []PathSummary{
			{Path: "/var/www/rec-test", User: "www-data", Status: "success"},
			{Path: "/opt/rec-test", Status: "success", RanAsRoot: true},
			{Path: "/etc/rec-fail", Status: "failed", Reason: "manifest missing"},
		},
	})

	for _, want := range []string{
		"✅",
		"<b>",
		"<code>repo@main</code>",
		host,
		"<code>759daa4</code>",
		"rdcstarr",
		"⚠ root",
		"<pre>",
		"/var/www/rec-test",
		"/opt/rec-test",
		"/etc/rec-fail",
		"failed",
		"└ manifest missing",
		buildinfo.Resolved(),
	} {
		if !strings.Contains(html, want) {
			t.Errorf("card missing %q:\n%s", want, html)
		}
	}
	if got := strings.Count(html, "⚠ root"); got != 1 {
		t.Errorf("root badge should mark exactly one path, got %d:\n%s", got, html)
	}
	if got := strings.Count(html, "<pre>"); got != 1 {
		t.Errorf("expected exactly one <pre> block, got %d:\n%s", got, html)
	}
	if strings.Contains(html, "second line ignored") {
		t.Errorf("card leaked the second commit-message line:\n%s", html)
	}

	start := strings.Index(html, "<pre>")
	end := strings.Index(html, "</pre>")
	if start == -1 || end == -1 {
		t.Fatalf("could not locate <pre>...</pre> block:\n%s", html)
	}
	pre := html[start : end+len("</pre>")]
	for _, path := range []string{"/var/www/rec-test", "/opt/rec-test", "/etc/rec-fail"} {
		if !strings.Contains(pre, path) {
			t.Errorf("<pre> block missing path %q:\n%s", path, pre)
		}
	}
	if strings.Contains(pre, "<code>") {
		t.Errorf("<pre> block must not nest <code> — it is already monospace:\n%s", pre)
	}
}

// TestRenderTelegramHTMLOmitsPreWhenNoPaths checks a Summary with no paths
// (e.g. the notify test probe) renders no empty <pre> block.
func TestRenderTelegramHTMLOmitsPreWhenNoPaths(t *testing.T) {
	html := RenderTelegramHTML(Summary{
		Repository: "repo",
		Ref:        "refs/heads/main",
		Status:     "test",
	})

	if strings.Contains(html, "<pre>") || strings.Contains(html, "</pre>") {
		t.Errorf("summary with no paths must not render a <pre> block:\n%s", html)
	}
	if !strings.Contains(html, "🧪 notification test") {
		t.Errorf("test notification does not use the dedicated Telegram card header:\n%s", html)
	}
}
