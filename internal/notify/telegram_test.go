package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/config"
)

func TestSendTelegramPostsChatIDAndText(t *testing.T) {
	var gotPath, gotChatID, gotText, gotParseMode string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotPath = r.URL.Path
		gotChatID = r.PostForm.Get("chat_id")
		gotText = r.PostForm.Get("text")
		gotParseMode = r.PostForm.Get("parse_mode")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	telegramAPIBase = srv.URL
	t.Cleanup(func() { telegramAPIBase = defaultTelegramAPIBase })

	cfg := config.TelegramConfig{Token: "123:SECRETTOKEN", ChatID: "-100200"}
	if err := sendTelegram(context.Background(), cfg, "<b>html</b>", "plain"); err != nil {
		t.Fatalf("sendTelegram: %v", err)
	}

	if want := "/bot123:SECRETTOKEN/sendMessage"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotChatID != "-100200" {
		t.Errorf("chat_id = %q, want %q", gotChatID, "-100200")
	}
	if gotText != "<b>html</b>" {
		t.Errorf("text = %q, want the html body sent first", gotText)
	}
	if gotParseMode != "HTML" {
		t.Errorf("parse_mode = %q, want HTML on the first attempt", gotParseMode)
	}
}

// The bot token travels in the URL path. Telegram's own error bodies and Go's
// *url.Error both hand it back to us; neither may reach a log line.
func TestSendTelegramErrorNeverLeaksToken(t *testing.T) {
	const token = "123:SECRETTOKEN"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	rejecting := srv.URL
	defer srv.Close()

	// A server that is not listening: http.Client returns a *url.Error whose
	// Error() embeds the full request URL, token included.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachable := dead.URL
	dead.Close()

	t.Cleanup(func() { telegramAPIBase = defaultTelegramAPIBase })

	for _, tc := range []struct {
		name string
		base string
		want string
	}{
		{name: "api rejects the token", base: rejecting, want: "Unauthorized"},
		{name: "transport fails", base: unreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			telegramAPIBase = tc.base

			err := sendTelegram(context.Background(), config.TelegramConfig{Token: token, ChatID: "1"}, "body", "body")
			if err == nil {
				t.Fatal("sendTelegram succeeded, want an error")
			}
			if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "SECRETTOKEN") {
				t.Errorf("error leaks the bot token: %v", err)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestSendTelegramFallsBackToPlainOn400 pins the fail-open contract: a
// malformed-entities 400 on the HTML attempt must not lose the notification —
// sendTelegram retries once with the plain body and no parse_mode.
func TestSendTelegramFallsBackToPlainOn400(t *testing.T) {
	var calls int
	var lastText string
	var sawParseModeKey bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		calls++
		lastText = r.PostForm.Get("text")
		_, sawParseModeKey = r.PostForm["parse_mode"]

		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: can't parse entities"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	telegramAPIBase = srv.URL
	t.Cleanup(func() { telegramAPIBase = defaultTelegramAPIBase })

	cfg := config.TelegramConfig{Token: "123:TOKEN", ChatID: "1"}
	if err := sendTelegram(context.Background(), cfg, "<b>broken", "plain fallback"); err != nil {
		t.Fatalf("sendTelegram: %v", err)
	}

	if calls != 2 {
		t.Fatalf("calls = %d, want exactly 2 (one retry)", calls)
	}
	if sawParseModeKey {
		t.Errorf("plain retry sent a parse_mode key, want none")
	}
	if lastText != "plain fallback" {
		t.Errorf("plain retry text = %q, want the plain body", lastText)
	}
}

// TestSendTelegramReturnsErrorWhenPlainRetryAlsoFails pins the "no loop"
// rule: a 400 on the plain retry itself is a final failure, not another
// retry.
func TestSendTelegramReturnsErrorWhenPlainRetryAlsoFails(t *testing.T) {
	var calls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: still broken"}`))
	}))
	defer srv.Close()

	telegramAPIBase = srv.URL
	t.Cleanup(func() { telegramAPIBase = defaultTelegramAPIBase })

	cfg := config.TelegramConfig{Token: "123:TOKEN", ChatID: "1"}
	err := sendTelegram(context.Background(), cfg, "<b>broken", "still broken")
	if err == nil {
		t.Fatal("sendTelegram succeeded, want an error")
	}
	if calls != 2 {
		t.Errorf("calls = %d, want exactly 2 (no loop after the second failure)", calls)
	}
}

// TestVerifyTelegramRejectsABadToken pins the point of verifying at all: a
// token that is merely well-formed is worth nothing, and the operator has to
// learn which half of the credentials the API refused.
func TestVerifyTelegramRejectsABadToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getMe") {
			t.Errorf("verification called %s before getMe succeeded", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"description":"Unauthorized"}`))
	}))
	defer srv.Close()

	telegramAPIBase = srv.URL
	defer func() { telegramAPIBase = defaultTelegramAPIBase }()

	const token = "1234:super-secret-bot-token"
	err := VerifyTelegram(context.Background(), config.TelegramConfig{Token: token, ChatID: "42"})
	if err == nil {
		t.Fatal("a rejected token verified successfully")
	}
	if !strings.Contains(err.Error(), "bot token") || !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("error %q does not name the token or the API's reason", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error leaks the bot token: %q", err)
	}
}

// TestVerifyTelegramRejectsAnUnreachableChat covers the failure a format check
// can never see: a valid token whose bot was never spoken to in that chat.
func TestVerifyTelegramRejectsAnUnreachableChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			_, _ = w.Write([]byte(`{"ok":true}`))

			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"description":"chat not found"}`))
	}))
	defer srv.Close()

	telegramAPIBase = srv.URL
	defer func() { telegramAPIBase = defaultTelegramAPIBase }()

	err := VerifyTelegram(context.Background(), config.TelegramConfig{Token: "1234:token", ChatID: "-100777"})
	if err == nil {
		t.Fatal("an unreachable chat verified successfully")
	}
	if !strings.Contains(err.Error(), "-100777") || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error %q does not name the chat or the API's reason", err)
	}
}

// TestVerifyTelegramAcceptsWorkingCredentials is the other half of the contract:
// verification must not stand in the way of credentials that do work.
func TestVerifyTelegramAcceptsWorkingCredentials(t *testing.T) {
	var called []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = append(called, r.URL.Path)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	telegramAPIBase = srv.URL
	defer func() { telegramAPIBase = defaultTelegramAPIBase }()

	if err := VerifyTelegram(context.Background(), config.TelegramConfig{Token: "1234:token", ChatID: "42"}); err != nil {
		t.Fatalf("VerifyTelegram: %v", err)
	}
	if len(called) != 2 || !strings.HasSuffix(called[0], "/getMe") || !strings.HasSuffix(called[1], "/getChat") {
		t.Errorf("verification did not probe both the token and the chat: %v", called)
	}
}

// TestVerifyTelegramSkipsTheChatProbeWhenThereIsNoChat covers a half-filled
// channel: a token with no chat ID is incomplete, not broken, and probing for a
// chat whose name is blank would report that the bot "cannot reach chat ".
func TestVerifyTelegramSkipsTheChatProbeWhenThereIsNoChat(t *testing.T) {
	var called []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = append(called, r.URL.Path)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	telegramAPIBase = srv.URL
	defer func() { telegramAPIBase = defaultTelegramAPIBase }()

	if err := VerifyTelegram(context.Background(), config.TelegramConfig{Token: "1234:token"}); err != nil {
		t.Fatalf("a token with no chat ID failed verification: %v", err)
	}
	if len(called) != 1 || !strings.HasSuffix(called[0], "/getMe") {
		t.Errorf("verification probed for a chat that was never configured: %v", called)
	}
}
