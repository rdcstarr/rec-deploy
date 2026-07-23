// Package cli holds cross-cutting process plumbing shared by every command:
// a signal-aware context, the diagnostic logger, and the error boundary.
package cli

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// Context returns a context cancelled on the first SIGINT or SIGTERM, so that
// Ctrl+C cleanly tears down any running work. The returned stop function
// releases the signal handler and must be deferred by the caller.
func Context() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// SetupLogger installs an slog text logger on stderr at level as the process
// default. This is diagnostic output only — user-facing output goes through
// internal/ui. The level is a parameter rather than a verbose bool because
// callers need more than the two-way CLI split: the serve daemon defaults to
// Info (its journal must show deploy lines even without -v), while every
// other command defaults to Warn and stays quiet.
func SetupLogger(level slog.Level) {
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

// Exit renders err at the process boundary and terminates with a non-zero
// status when it is non-nil. A nil error is a clean exit.
func Exit(err error) {
	if err == nil {
		return
	}

	ui.RenderError(err)
	os.Exit(1)
}
