package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/notify"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newNotifyCmd builds the `notify` group: probing the configured notification
// channels.
func newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Probe the configured notification channels",
		Long:  "notify sends a probe through every notification channel and reports each outcome — the terminal, not journalctl, is where an operator learns why a channel is silent.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isInteractive() {
				return cmd.Help()
			}

			return notifyMenu(cmd)
		},
	}

	cmd.AddCommand(newNotifyTestCmd())

	return cmd
}

// notifyMenu is the interactive hub for the notify group. The group's top menu
// returns ui.ErrBack, so ← climbs to the rec-deploy hub.
func notifyMenu(cmd *cobra.Command) error {
	return (ui.Menu{
		Title: ui.ScreenPath("rec-deploy", "Notify"),
		Options: func() []ui.Option {
			return []ui.Option{
				{Label: "test " + ui.Dim("send a probe through every configured channel"), Value: "test"},
			}
		},
		Help:   func() string { return commandHelp(cmd) },
		Handle: func(choice string) error { return dispatch(cmd, choice) },
	}).Run()
}

// newNotifyTestCmd builds `notify test`.
func newNotifyTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "test",
		Short:   "Send a probe through every configured channel",
		Args:    cobra.NoArgs,
		Example: "  rec-deploy notify test",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNotifyTest(cmd.Context())
		},
	}
}

// runNotifyTest sends a probe through every channel and reports each outcome —
// the terminal, not journalctl, is where an operator learns why a channel is
// silent.
func runNotifyTest(ctx context.Context) error {
	cfg := Config()
	results := notify.Deliver(ctx, cfg.Notify, notify.Summary{
		Repository: "rec-deploy",
		Ref:        "refs/heads/main",
		Status:     "test",
		Message:    "Notifications are configured correctly.",
	})

	return notifyTestOutcome(results)
}

// notifyTestOutcome renders results for the current output mode and returns
// the error that ends the command: nil unless a channel failed. It is split
// out of runNotifyTest, which calls the network-touching notify.Deliver, so
// the JSON-vs-failure exit contract is testable against a canned []ChannelResult.
// House precedent (report in cmd/deploy.go): --json prints first and stdout
// stays a pure value; the failure still returns as the command's error, which
// renders on stderr.
func notifyTestOutcome(results []notify.ChannelResult) error {
	if flagJSON {
		if err := ui.PrintJSON(results); err != nil {
			return err
		}
		if failed := channelFailures(results); failed > 0 {
			return fmt.Errorf("%d channel(s) failed — the errors above name the cause", failed)
		}

		return nil
	}

	if failed := printChannelResults(results); failed > 0 {
		return fmt.Errorf("%d channel(s) failed — the errors above name the cause", failed)
	}

	return nil
}

// channelFailures counts how many results carry a send error, without
// printing — the JSON branch of notifyTestOutcome needs the count but not
// printChannelResults' human-readable lines.
func channelFailures(results []notify.ChannelResult) (failed int) {
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}

	return failed
}

// printChannelResults prints one line per channel result — sent, not
// configured (naming the missing fields), or failed (carrying the send
// error) — and returns how many channels failed. `notify test` and init's
// offerTestNotification share it, so an operator sees the same wording
// either way.
func printChannelResults(results []notify.ChannelResult) (failed int) {
	for _, r := range results {
		switch {
		case r.Skipped:
			ui.Info("› " + r.Channel + ": not configured — " + r.Detail)
		case r.Err != nil:
			failed++
			ui.Warn(r.Channel + ": failed — " + r.Detail)
		default:
			ui.Success(r.Channel + ": sent")
		}
	}

	return failed
}
