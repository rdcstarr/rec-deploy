package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/notify"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newNotificationsCmd builds the `notifications` group: the settings of every
// notification channel, and probing them. The channels used to be edited under
// `config` and tested from a top-level `notify`, which put a channel's settings
// and the button that exercises them in two different commands.
func newNotificationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "notifications",
		// The group answered to `notify` for its whole life, and that is what
		// any script or muscle memory reaches for.
		Aliases: []string{"notify"},
		Short:   "Configure and probe the notification channels",
		Long:    "notifications holds each channel's settings and sends probes through them, reporting every outcome — the terminal, not journalctl, is where an operator learns why a channel is silent.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isInteractive() {
				return cmd.Help()
			}

			return notificationsMenu(cmd)
		},
	}

	cmd.AddCommand(newNotificationsTestCmd())

	return cmd
}

// notifyChannels are the notification channels as the menu presents them. They
// are configSections — the fields, editor and validation are shared with
// `config` — listed here rather than there because this is where they are now
// reached from.
var notifyChannels = []configSection{
	{Key: "telegram", Title: "Telegram", Description: "send deploy results to a Telegram chat"},
	{Key: "email", Title: "Email", Description: "send deploy results by email"},
}

// notificationsMenu is the interactive hub for the group. The group's top menu
// returns ui.ErrBack, so ← climbs to the rec-deploy hub.
func notificationsMenu(cmd *cobra.Command) error {
	return (ui.Menu{
		Title: ui.ScreenPath("rec-deploy", "Notifications"),
		Options: func() []ui.Option {
			cfg := Config()
			items := make([]ui.DescribedOption, 0, len(notifyChannels))
			for _, channel := range notifyChannels {
				items = append(items, ui.DescribedOption{Name: channel.Title, Description: channelState(cfg, channel.Key), Value: channel.Key})
			}
			items = append(items, ui.DescribedOption{Name: "Test all", Description: "send a probe through every configured channel", Value: "test"})

			return append(ui.DescribedOptions(items...), ui.Option{Label: "Exit", Value: "exit"})
		},
		Help:       func() string { return commandHelp(cmd) },
		BackValues: map[string]bool{"exit": true},
		Handle: func(choice string) error {
			if choice == "test" {
				return dispatch(cmd, choice)
			}

			return openNotifyChannel(cmd, choice)
		},
	}).Run()
}

// channelState summarises a channel for the hub, so the operator sees which
// ones are live without opening each.
func channelState(cfg *config.Config, channel string) string {
	switch channel {
	case "telegram":
		if cfg.Notify.Telegram.Configured() {
			return "configured"
		}

		return notify.MissingTelegram(cfg.Notify.Telegram)
	case "email":
		if cfg.Notify.Email.Configured() {
			return "configured"
		}

		return notify.MissingEmail(cfg.Notify.Email)
	}

	// A channel added to notifyChannels and not wired here describes itself as
	// nothing, rather than borrowing another channel's state.
	return ""
}

// openNotifyChannel opens one channel's settings, with its own test beside them.
func openNotifyChannel(cmd *cobra.Command, channel string) error {
	return openSettingsSection(cmd, channel, ui.ScreenPath("rec-deploy", "Notifications", configSectionTitle(channel)))
}

// newNotificationsTestCmd builds `notifications test`.
func newNotificationsTestCmd() *cobra.Command {
	var channel string
	cmd := &cobra.Command{
		Use:     "test",
		Short:   "Send a probe through the notification channels",
		Args:    cobra.NoArgs,
		Example: "  rec-deploy notifications test\n  rec-deploy notifications test --channel telegram",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNotifyTest(cmd.Context(), channel)
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "probe only this channel: telegram or email")

	return cmd
}

// runNotifyTest sends a probe and reports each outcome — the terminal, not
// journalctl, is where an operator learns why a channel is silent. An empty
// channel probes every one of them.
func runNotifyTest(ctx context.Context, channel string) error {
	cfg := Config()
	summary := notify.Summary{
		Repository: "rec-deploy",
		Ref:        "refs/heads/main",
		Status:     "test",
		Message:    "Notifications are configured correctly.",
	}

	// Delivery reaches Telegram and an SMTP server, several seconds in the worst
	// case, so it shows progress rather than sitting on a dead pause. The spinner
	// clears before the per-channel results print.
	var results []notify.ChannelResult
	if err := ui.Spinner("Sending a test notification…", func() error {
		switch channel {
		case "":
			results = notify.Deliver(ctx, cfg.Notify, summary)
		case "telegram":
			results = []notify.ChannelResult{notify.DeliverTelegram(ctx, cfg.Notify.Telegram, summary)}
		case "email":
			results = []notify.ChannelResult{notify.DeliverEmail(ctx, cfg.Notify.Email, summary)}
		default:
			return fmt.Errorf("unknown channel %q — use `telegram` or `email`", channel)
		}

		return nil
	}); err != nil {
		return err
	}

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
// error) — and returns how many channels failed. `notifications test` and init's
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
