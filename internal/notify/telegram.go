package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/config"
)

// defaultTelegramAPIBase is the Telegram Bot API origin.
const defaultTelegramAPIBase = "https://api.telegram.org"

// telegramAPIBase is the API origin requests are sent to. It is a var so tests
// can point the sender at an httptest.Server.
var telegramAPIBase = defaultTelegramAPIBase

// telegramClient sends the notification. The timeout is what stops a hung
// Telegram from holding a deploy's final report open forever.
var telegramClient = &http.Client{Timeout: 15 * time.Second}

// errTelegramBadRequest marks a Bot API HTTP 400 (malformed entities) — the
// only failure sendTelegram treats as retryable with the plain body.
var errTelegramBadRequest = errors.New("telegram rejected the request")

// VerifyTelegram reports whether the bot token is valid and whether the bot can
// reach the configured chat, without sending a message. It is the notification
// counterpart of validating the GitHub token against GET /user: credentials
// that merely look well-formed are worth nothing on the deploy that needed them,
// and a chat ID is only ever wrong in ways no format check can see.
func VerifyTelegram(ctx context.Context, cfg config.TelegramConfig) error {
	if err := callTelegram(ctx, cfg.Token, "getMe", nil); err != nil {
		return fmt.Errorf("the bot token was rejected — %w; create one with @BotFather", err)
	}

	// With no chat there is nothing to probe, and asking anyway would report
	// that the bot "cannot reach chat" with the name left blank. A half-filled
	// channel is Configured()'s business, and the caller warns about it.
	if cfg.ChatID == "" {
		return nil
	}

	if err := callTelegram(ctx, cfg.Token, "getChat", url.Values{"chat_id": {cfg.ChatID}}); err != nil {
		return fmt.Errorf("the bot cannot reach chat %s — %w; send the bot one message from that chat first, or check the id with @userinfobot", cfg.ChatID, err)
	}

	return nil
}

// callTelegram posts one Bot API method and reports the outcome as the API's own
// description. Like postTelegram it must never name the token: the token is in
// the URL path, so both an http.Client failure and the echoed response body can
// carry it.
func callTelegram(ctx context.Context, token, method string, form url.Values) error {
	endpoint := fmt.Sprintf("%s/bot%s/%s", telegramAPIBase, token, method)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return redactURLError(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := telegramClient.Do(req)
	if err != nil {
		return redactURLError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New(telegramError(resp))
	}

	return nil
}

// sendTelegram posts the HTML card and falls back to the plain body when the
// Bot API rejects the entities (400) — a notification must never be lost to a
// template.
func sendTelegram(ctx context.Context, cfg config.TelegramConfig, htmlText, plain string) error {
	err := postTelegram(ctx, cfg, htmlText, "HTML")
	if err == nil {
		return nil
	}
	if !errors.Is(err, errTelegramBadRequest) {
		return err
	}
	slog.Error("telegram rejected the html card — sending plain text", "error", err)

	return postTelegram(ctx, cfg, plain, "")
}

// postTelegram posts one message to the Bot API, setting parse_mode when
// non-empty. On HTTP 400 it wraps errTelegramBadRequest so sendTelegram knows
// to retry with the plain body; every other failure is returned as-is.
//
// Telegram authenticates by putting the bot token in the URL path, so every
// error here is a token-leak hazard: the response body echoes the request, and
// http.Client wraps failures in a *url.Error whose message embeds the full URL.
// No error this function returns may name the token — callers log them.
func postTelegram(ctx context.Context, cfg config.TelegramConfig, text, parseMode string) error {
	form := url.Values{}
	form.Set("chat_id", cfg.ChatID)
	form.Set("text", text)
	if parseMode != "" {
		form.Set("parse_mode", parseMode)
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, cfg.Token)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram sendMessage: %w", redactURLError(err))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := telegramClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram sendMessage: %w", redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusBadRequest {
		return fmt.Errorf("telegram sendMessage: %s: %w", telegramError(resp), errTelegramBadRequest)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram sendMessage: %s", telegramError(resp))
	}

	return nil
}

// redactURLError unwraps a *url.Error down to its cause. url.Error.Error()
// renders the request URL, which carries the bot token in its path; the cause
// alone ("connection refused", "context deadline exceeded") carries no secret.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}

	return err
}

// telegramError renders a rejected response as the Bot API's own description
// plus the status, so the operator sees "Unauthorized (HTTP 401)" and not
// "HTTP 401".
func telegramError(resp *http.Response) string {
	var e struct {
		Description string `json:"description"`
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if json.Unmarshal(body, &e) == nil && e.Description != "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Description, resp.StatusCode)
	}

	return fmt.Sprintf("HTTP %d", resp.StatusCode)
}
