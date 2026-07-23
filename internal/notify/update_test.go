package notify

import (
	"strings"
	"testing"
)

// TestRenderUpdateHTMLUpdated checks the success card: the self-update command
// line, the green "updated" verdict, the unit that restarted, and the server
// named in both the chrome and the footer — with no failure livery.
func TestRenderUpdateHTMLUpdated(t *testing.T) {
	html, err := RenderUpdateHTML(UpdateSummary{
		From: "v1.1.0", To: "v1.1.1", Unit: "rec-deploy.service", Outcome: Updated,
	}, "srv-01")
	if err != nil {
		t.Fatalf("RenderUpdateHTML: %v", err)
	}

	for _, want := range []string{
		"$ self-update v1.1.0 → v1.1.1",
		"✓ updated",
		"rec-deploy.service restarted",
		"rec-deploy — self-update",
		"· srv-01",
		"#23272c", // normal border
	} {
		if !strings.Contains(html, want) {
			t.Errorf("updated card missing %q", want)
		}
	}
	if strings.Contains(html, "#3a2724") {
		t.Errorf("updated card unexpectedly wears the failure border")
	}
}

// TestRenderUpdateHTMLInstalled checks the neutral card: the amber "installed"
// verdict and the systemctl hint, and that it never borrows the failure livery.
func TestRenderUpdateHTMLInstalled(t *testing.T) {
	html, err := RenderUpdateHTML(UpdateSummary{
		From: "v1.1.0", To: "v1.1.1", Unit: "rec-deploy.service", Outcome: Installed,
	}, "srv-01")
	if err != nil {
		t.Fatalf("RenderUpdateHTML: %v", err)
	}

	for _, want := range []string{"› installed", "systemctl start rec-deploy.service"} {
		if !strings.Contains(html, want) {
			t.Errorf("installed card missing %q", want)
		}
	}
	if strings.Contains(html, "#3a2724") {
		t.Errorf("installed card unexpectedly wears the failure border")
	}
}

// TestRenderUpdateHTMLRolledBack checks the failure card: the red verdict, the
// failure border, and that the rollback detail is HTML-escaped — it is an error
// string, never trusted markup.
func TestRenderUpdateHTMLRolledBack(t *testing.T) {
	html, err := RenderUpdateHTML(UpdateSummary{
		From: "v1.1.0", To: "v1.1.1", Outcome: RolledBack,
		Detail: `boom <script>alert(1)</script>`,
	}, "srv-01")
	if err != nil {
		t.Fatalf("RenderUpdateHTML: %v", err)
	}

	for _, want := range []string{"! rolled back", "#3a2724", "&lt;script&gt;"} {
		if !strings.Contains(html, want) {
			t.Errorf("rolled-back card missing %q", want)
		}
	}
	if strings.Contains(html, "<script>") {
		t.Errorf("rollback detail markup survived escaping:\n%s", html)
	}
	if strings.Contains(html, "✓ updated") {
		t.Errorf("rolled-back card claims success")
	}
}

// TestRenderUpdatePlainNamesServer checks the plain body every Telegram send and
// email fallback carries the version transition and the server name — the latter
// because Telegram delivers only the body.
func TestRenderUpdatePlainNamesServer(t *testing.T) {
	body := renderUpdatePlain(UpdateSummary{
		From: "v1.1.0", To: "v1.1.1", Unit: "rec-deploy.service", Outcome: Updated,
	}, "srv-01")

	for _, want := range []string{
		"rec-deploy v1.1.0 → v1.1.1; rec-deploy.service restarted.",
		"host: srv-01",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plain body missing %q:\n%s", want, body)
		}
	}
}

// TestRenderUpdatePlainRollbackNamesServerInline checks the rollback body names
// the server in its sentence (not a trailing host line) and carries the detail.
func TestRenderUpdatePlainRollbackNamesServerInline(t *testing.T) {
	body := renderUpdatePlain(UpdateSummary{
		From: "v1.1.0", To: "v1.1.1", Outcome: RolledBack, Detail: "the daemon would not start",
	}, "srv-01")

	if !strings.Contains(body, "failed on srv-01.") {
		t.Errorf("rollback body does not name the server inline:\n%s", body)
	}
	if !strings.Contains(body, "the daemon would not start") {
		t.Errorf("rollback body missing the detail:\n%s", body)
	}
	if strings.Contains(body, "host: srv-01") {
		t.Errorf("rollback body should name the server inline, not with a host line:\n%s", body)
	}
}

// TestUpdateSubjectNamesServer checks the headline names the outcome and the
// server.
func TestUpdateSubjectNamesServer(t *testing.T) {
	if got := updateSubject(UpdateSummary{To: "v1.1.1", Outcome: Updated}, "srv-01"); got != "rec-deploy: updated to v1.1.1 on srv-01" {
		t.Errorf("updateSubject = %q", got)
	}
	if got := updateSubject(UpdateSummary{To: "v1.1.1", Outcome: Updated}, ""); got != "rec-deploy: updated to v1.1.1" {
		t.Errorf("updateSubject with no host = %q", got)
	}
}

func TestRenderUpdateTelegramHTML(t *testing.T) {
	got := RenderUpdateTelegramHTML(UpdateSummary{
		From: "v1.1.1", To: "v1.2.2", Unit: "rec-deploy.service", Outcome: Updated,
	}, "deploy-01")
	for _, want := range []string{
		"<b>✅ rec-deploy updated</b>",
		"<code>deploy-01</code>",
		"<code>v1.1.1 → v1.2.2</code>",
		"▸ rec-deploy.service restarted",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Telegram update card missing %q:\n%s", want, got)
		}
	}
}

func TestRenderUpdateTelegramHTMLEscapesValues(t *testing.T) {
	got := RenderUpdateTelegramHTML(UpdateSummary{
		From: "v1", To: "v2", Outcome: RolledBack, Detail: "failed <unsafe>",
	}, "host&one")
	if strings.Contains(got, "<unsafe>") || !strings.Contains(got, "failed &lt;unsafe&gt;") || !strings.Contains(got, "host&amp;one") {
		t.Errorf("Telegram update card did not escape dynamic values:\n%s", got)
	}
}
