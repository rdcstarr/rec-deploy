package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/notify"
)

// TestPrintChannelResults covers the two outcomes that must never look alike: a
// skipped channel names its missing fields, and a failed channel carries the
// send error's text — the failed count is what drives `notifications test`'s exit
// code.
func TestPrintChannelResults(t *testing.T) {
	var failed int
	out := capture(t, func() {
		failed = printChannelResults([]notify.ChannelResult{
			{Channel: "telegram", Skipped: true, Detail: "bot token and chat id are not set"},
			{Channel: "email", Err: errors.New("dial tcp: timeout"), Detail: "dial tcp: timeout"},
		})
	})

	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if !strings.Contains(out, "telegram: not configured — bot token and chat id are not set") {
		t.Errorf("output %q does not report the skipped channel", out)
	}
	if !strings.Contains(out, "email: failed — dial tcp: timeout") {
		t.Errorf("output %q does not report the failed channel", out)
	}
}

// TestPrintChannelResultsAllSent covers the all-clear case: every channel
// reads "sent" and the failed count is zero.
func TestPrintChannelResultsAllSent(t *testing.T) {
	var failed int
	out := capture(t, func() {
		failed = printChannelResults([]notify.ChannelResult{
			{Channel: "telegram"},
			{Channel: "email"},
		})
	})

	if failed != 0 {
		t.Errorf("failed = %d, want 0", failed)
	}
	if !strings.Contains(out, "telegram: sent") || !strings.Contains(out, "email: sent") {
		t.Errorf("output %q does not report both channels sent", out)
	}
}

// TestRunNotifyTestNoConfig is the safe-smoke contract as a test: with no
// channel configured, `notifications test` reports two skipped channels and
// returns nil — a missing configuration is not itself a failure.
func TestRunNotifyTestNoConfig(t *testing.T) {
	previous := cfg
	cfg = &config.Config{}
	defer func() { cfg = previous }()

	var runErr error
	out := capture(t, func() {
		runErr = runNotifyTest(context.Background(), "")
	})

	if runErr != nil {
		t.Fatalf("runNotifyTest: %v", runErr)
	}
	if strings.Count(out, "not configured") != 2 {
		t.Errorf("output %q does not report exactly two unconfigured channels", out)
	}
}

// TestRunNotifyTestProbesOneChannel is the defect this group was reshaped for:
// a test opened from the Telegram screen reached Telegram *and* email, so an
// operator checking one channel notified the other. Each channel now probes
// itself and nothing else, and an unknown one is an error rather than a
// silent fan-out.
func TestRunNotifyTestProbesOneChannel(t *testing.T) {
	previous := cfg
	cfg = &config.Config{}
	defer func() { cfg = previous }()

	for _, channel := range []string{"telegram", "email"} {
		var runErr error
		out := capture(t, func() {
			runErr = runNotifyTest(context.Background(), channel)
		})

		if runErr != nil {
			t.Fatalf("runNotifyTest(%q): %v", channel, runErr)
		}
		if strings.Count(out, "not configured") != 1 || !strings.Contains(out, channel) {
			t.Errorf("a %s test reported %q, want that channel alone", channel, out)
		}
	}

	var runErr error
	capture(t, func() { runErr = runNotifyTest(context.Background(), "carrier-pigeon") })
	if runErr == nil {
		t.Error("an unknown channel silently probed something instead of failing")
	}
}

// TestChannelStateDescribesEveryListedChannel keeps the hub honest: each entry
// in notifyChannels says whether it is live and, when it is not, which field is
// missing — a blank line beside a channel name tells the operator nothing.
func TestChannelStateDescribesEveryListedChannel(t *testing.T) {
	unset := &config.Config{}
	set := &config.Config{Notify: config.NotifyConfig{
		Telegram: config.TelegramConfig{Token: "t", ChatID: "1"},
		Email:    config.EmailConfig{SMTP: "smtp.example.com:587", From: "a@example.com", To: "b@example.com"},
	}}

	for _, channel := range notifyChannels {
		if state := channelState(unset, channel.Key); !strings.Contains(state, "not set") {
			t.Errorf("%s unconfigured reads %q, want the missing fields named", channel.Key, state)
		}
		if state := channelState(set, channel.Key); state != "configured" {
			t.Errorf("%s configured reads %q, want \"configured\"", channel.Key, state)
		}
	}
}

// TestNotifyTestOutcomeJSONFailure pins the contract a script relies on: under
// --json, a failed channel still fails the command (non-nil error) even
// though stdout carries nothing but the results array — the caller cannot
// tell success from failure by exit code alone otherwise.
func TestNotifyTestOutcomeJSONFailure(t *testing.T) {
	previous := flagJSON
	flagJSON = true
	defer func() { flagJSON = previous }()

	results := []notify.ChannelResult{
		{Channel: "telegram"},
		{Channel: "email", Err: errors.New("dial tcp: timeout"), Detail: "dial tcp: timeout"},
	}

	var outcomeErr error
	out := capture(t, func() {
		outcomeErr = notifyTestOutcome(results)
	})

	if outcomeErr == nil {
		t.Fatal("notifyTestOutcome = nil error, want non-nil for a failed channel")
	}
	if !strings.Contains(outcomeErr.Error(), "1 channel(s) failed") {
		t.Errorf("error %q does not name the failure count", outcomeErr)
	}

	var decoded []notify.ChannelResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("stdout is not a pure JSON array: %v\noutput: %s", err, out)
	}
	if len(decoded) != 2 {
		t.Errorf("decoded %d results, want 2", len(decoded))
	}
}

// TestNotifyTestOutcomeJSONAllSent covers the all-clear case under --json: no
// failed channel, nil error.
func TestNotifyTestOutcomeJSONAllSent(t *testing.T) {
	previous := flagJSON
	flagJSON = true
	defer func() { flagJSON = previous }()

	results := []notify.ChannelResult{
		{Channel: "telegram"},
		{Channel: "email"},
	}

	var outcomeErr error
	capture(t, func() {
		outcomeErr = notifyTestOutcome(results)
	})

	if outcomeErr != nil {
		t.Errorf("notifyTestOutcome = %v, want nil when nothing failed", outcomeErr)
	}
}
