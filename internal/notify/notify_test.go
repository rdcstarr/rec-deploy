package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/config"
)

func TestRenderFlagsRootPaths(t *testing.T) {
	body := Render(Summary{
		Repository: "rdcstarr/tema",
		Ref:        "refs/heads/main",
		SHA:        "abc1234",
		Message:    "fix: thing",
		Author:     "Andrei",
		Status:     "success",
		Paths: []PathSummary{
			{Path: "/home/andrei/web/site/public_html/wp-content/themes/tema", User: "andrei", Status: "success"},
			{Path: "/var/www/api", User: "root", Status: "success", RanAsRoot: true},
		},
	})

	if !strings.Contains(body, "rdcstarr/tema") || !strings.Contains(body, "abc1234") {
		t.Errorf("body lost the repository or the sha:\n%s", body)
	}
	// Push access to a root-owned target is root on the server. That must be
	// visible in the notification, not discovered later.
	if !strings.Contains(body, "⚠ root") {
		t.Errorf("a root-owned path is not flagged:\n%s", body)
	}
	if !strings.Contains(body, "/var/www/api") {
		t.Errorf("body lost a path:\n%s", body)
	}
}

// Zero installations is a reported failure, never silence.
func TestRenderZeroInstallations(t *testing.T) {
	body := Render(Summary{
		Repository: "rdcstarr/tema",
		Ref:        "refs/heads/main",
		Status:     "failed",
		Error:      "no installation of rdcstarr/tema found under the discovery roots",
	})

	if !strings.Contains(body, "no installation") {
		t.Errorf("body lost the error:\n%s", body)
	}
	if !strings.Contains(strings.ToLower(body), "failed") {
		t.Errorf("body does not say it failed:\n%s", body)
	}
}

func TestRenderSkipReason(t *testing.T) {
	body := Render(Summary{
		Repository: "rdcstarr/tema",
		Ref:        "refs/heads/develop",
		Status:     "skipped",
		Paths: []PathSummary{
			{Path: "/srv/tema", User: "deploy", Status: "skipped", Reason: "checkout is on main, push was to develop"},
		},
	})

	if !strings.Contains(body, "push was to develop") {
		t.Errorf("body lost the skip reason:\n%s", body)
	}
}

func TestSubject(t *testing.T) {
	got := Subject(Summary{Repository: "rdcstarr/tema", Ref: "refs/heads/main", Status: "failed"})

	if !strings.Contains(got, "rdcstarr/tema") || !strings.Contains(got, "main") || !strings.Contains(strings.ToLower(got), "failed") {
		t.Errorf("Subject = %q", got)
	}
}

// TestSendUpdateWithNoChannelsConfigured: journald always gets it and the
// optional channels are skipped. Best-effort means it never panics and never
// blocks — an update notification must not be able to fail an update that
// succeeded.
func TestSendUpdateWithNoChannelsConfigured(t *testing.T) {
	done := make(chan struct{})

	go func() {
		SendUpdate(context.Background(), config.NotifyConfig{}, UpdateSummary{
			From: "v0.2.0", To: "v0.3.0", Unit: "rec-deploy.service", Outcome: Updated,
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SendUpdate blocked with no channels configured")
	}
}

// TestDeliverSkipsUnconfiguredChannelsWithExactMissingFields pins the
// contract `notify test` relies on: a skipped channel's Detail names exactly
// which fields are missing, not a generic "not configured".
func TestDeliverSkipsUnconfiguredChannelsWithExactMissingFields(t *testing.T) {
	results := Deliver(context.Background(), config.NotifyConfig{}, Summary{
		Repository: "o/r", Ref: "refs/heads/main", Status: "success",
	})

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}

	tg := results[0]
	if tg.Channel != "telegram" || !tg.Skipped {
		t.Fatalf("results[0] = %+v, want telegram, Skipped", tg)
	}
	if want := "bot token and chat id are not set"; tg.Detail != want {
		t.Errorf("telegram Detail = %q, want %q", tg.Detail, want)
	}

	em := results[1]
	if em.Channel != "email" || !em.Skipped {
		t.Fatalf("results[1] = %+v, want email, Skipped", em)
	}
	if want := "smtp, from and to are not set"; em.Detail != want {
		t.Errorf("email Detail = %q, want %q", em.Detail, want)
	}
}

// TestDeliverReportsEmailSendFailureWithoutSkipping pins the "attempted but
// failed" shape: Skipped stays false, Err is set, and Detail carries the
// error text — an operator running `notify test` sees why, not just that it
// failed.
func TestDeliverReportsEmailSendFailureWithoutSkipping(t *testing.T) {
	cfg := config.NotifyConfig{
		Email: config.EmailConfig{SMTP: "127.0.0.1:1", From: "deploy@example.com", To: "ops@example.com"},
	}

	results := Deliver(context.Background(), cfg, Summary{Repository: "o/r", Ref: "refs/heads/main", Status: "success"})
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}

	tg := results[0]
	if tg.Channel != "telegram" || !tg.Skipped {
		t.Errorf("telegram result = %+v, want Skipped (not configured)", tg)
	}

	em := results[1]
	if em.Channel != "email" || em.Skipped {
		t.Fatalf("email result = %+v, want an attempted send, not Skipped", em)
	}
	if em.Err == nil {
		t.Error("email Err = nil, want a connection failure")
	}
	if em.Detail == "" {
		t.Error("email Detail is empty, want the send error text")
	}
}

// TestDeliverSendsTelegramSuccessfully pins the clean-send shape against the
// httptest seam: Skipped false, Err nil.
func TestDeliverSendsTelegramSuccessfully(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	telegramAPIBase = srv.URL
	t.Cleanup(func() { telegramAPIBase = defaultTelegramAPIBase })

	cfg := config.NotifyConfig{Telegram: config.TelegramConfig{Token: "123:TOKEN", ChatID: "1"}}
	results := Deliver(context.Background(), cfg, Summary{Repository: "o/r", Ref: "refs/heads/main", Status: "success"})
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}

	tg := results[0]
	if tg.Channel != "telegram" || tg.Skipped {
		t.Fatalf("telegram result = %+v, want an attempted send", tg)
	}
	if tg.Err != nil {
		t.Errorf("telegram Err = %v, want nil", tg.Err)
	}

	em := results[1]
	if em.Channel != "email" || !em.Skipped {
		t.Errorf("email result = %+v, want Skipped (not configured)", em)
	}
}
