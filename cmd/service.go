package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/systemd"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newServiceCmd builds the `service` group: the lifecycle of the systemd unit
// that runs the webhook daemon. It is the sibling of `serve`, which is the
// daemon process itself — one runs the daemon in the foreground, the other
// manages the unit systemd runs it under.
//
// These actions used to live under the status report, as a menu that opened
// after the report had already answered what the operator asked. A command of
// their own is what lets them be scripted and lets the status report end.
func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "service",
		Short:   "Start, stop or restart the webhook daemon",
		Long:    "service manages " + daemonUnit + ", the systemd unit that runs the webhook daemon on this server.",
		Args:    cobra.NoArgs,
		Example: "rec-deploy service restart\nrec-deploy service stop --yes",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isInteractive() {
				return cmd.Help()
			}

			return serviceMenu(cmd)
		},
	}

	cmd.AddCommand(
		newServiceActionCmd("start", "Start the webhook daemon"),
		newServiceActionCmd("stop", "Stop the webhook daemon until it is started again"),
		newServiceActionCmd("restart", "Restart the webhook daemon"),
	)

	return cmd
}

// newServiceActionCmd builds one lifecycle leaf. The three differ only in the
// verb they hand to systemd and in whether they need a confirmation, so they
// share a constructor rather than repeating the same body three times.
func newServiceActionCmd(action, short string) *cobra.Command {
	return &cobra.Command{
		Use:     action,
		Short:   short,
		Args:    cobra.NoArgs,
		Example: "rec-deploy service " + action,
		RunE: func(cmd *cobra.Command, args []string) error {
			return serviceAction(cmd.Context(), action)
		},
	}
}

// serviceMenu is the interactive hub for the service group. It offers only the
// transition that applies — a running daemon has nothing to start — so it needs
// the daemon's current state before it can draw anything.
func serviceMenu(cmd *cobra.Command) error {
	state := currentDaemonLifecycle(cmd.Context())
	if state == daemonUnmanaged {
		return fmt.Errorf("systemd does not manage %s on this host — run the daemon in the foreground with `rec-deploy serve`", daemonUnit)
	}

	return (ui.Menu{
		Title: ui.ScreenPath("rec-deploy", "Service"),
		Options: func() []ui.Option {
			return ui.DescribedOptions(lifecycleOptions(currentDaemonLifecycle(cmd.Context()))...)
		},
		Help:   func() string { return commandHelp(cmd) },
		Handle: func(choice string) error { return dispatch(cmd, choice) },
	}).Run()
}

// daemonLifecycle is the state lifecycleOptions offers actions for: whether
// systemd manages the daemon at all, and if it does, whether the daemon is
// running. Splitting this out lets the invariant — never offer both start and
// stop — be tested by passing a state directly, instead of only through the
// real systemd.Available() a dev box without systemd always resolves the same
// way.
type daemonLifecycle int

const (
	daemonUnmanaged daemonLifecycle = iota // no systemd on this host: nothing to offer
	daemonActive                           // systemd manages it and it is running
	daemonInactive                         // systemd manages it and it is not running
)

// currentDaemonLifecycle reads daemonLifecycle off the host.
func currentDaemonLifecycle(ctx context.Context) daemonLifecycle {
	if !systemd.Available() {
		return daemonUnmanaged
	}
	if systemd.IsActive(ctx, daemonUnit) {
		return daemonActive
	}

	return daemonInactive
}

// lifecycleOptions is the pure half of serviceMenu: given the daemon's state,
// decide which service actions apply. A running daemon has nothing to start; a
// stopped one has nothing to stop or restart.
func lifecycleOptions(state daemonLifecycle) []ui.DescribedOption {
	switch state {
	case daemonActive:
		return []ui.DescribedOption{
			{Name: "restart", Description: "restart the webhook daemon", Value: "restart"},
			{Name: "stop", Description: "stop the webhook daemon until it is started again", Value: "stop"},
		}
	case daemonInactive:
		return []ui.DescribedOption{
			{Name: "start", Description: "start the webhook daemon", Value: "start"},
		}
	default:
		return nil
	}
}

// serviceAction starts, stops or restarts the webhook daemon. Stopping and
// restarting interrupt whatever the daemon is doing, so they confirm first —
// and, with no terminal to confirm in, require --yes like every other
// destructive action here.
func serviceAction(ctx context.Context, action string) error {
	if action != "start" && !flagYes {
		if !isInteractive() {
			return fmt.Errorf("a %s of %s cuts short a deploy running right now — re-run with `--yes`", action, daemonUnit)
		}

		ok, err := ui.Confirm("Really "+action+" "+daemonUnit+"?", "A deploy running right now is cut short. Its delivery is already spent, so GitHub will not send it again.")
		if err != nil {
			return err
		}
		if !ok {
			return ui.ErrBack // declined — nothing was done, so the menu stays
		}
	}

	var run func() error
	var title string
	switch action {
	case "start":
		run, title = func() error { return systemd.Start(ctx, daemonUnit) }, "Starting "+daemonUnit+"…"
	case "stop":
		run, title = func() error { return systemd.Stop(ctx, daemonUnit) }, "Stopping "+daemonUnit+"…"
	case "restart":
		run, title = func() error { return systemd.Restart(ctx, daemonUnit) }, "Restarting "+daemonUnit+"…"
	default:
		return fmt.Errorf("unknown service action %q", action)
	}

	// Stopping and restarting drain in-flight deploy work before the unit
	// comes back down or up, so this is plausibly multi-second — a dead pause
	// between "Yes" and the result without the spinner.
	if err := ui.Spinner(title, run); err != nil {
		// systemd refuses a non-root caller with "interactive authentication
		// required", which is accurate and says nothing about what to do.
		// euid only shapes the message — it is not a gate: an operator whose
		// polkit rule grants manage-units without authentication succeeded
		// above and never reaches this line.
		if os.Geteuid() != 0 {
			return fmt.Errorf("%w — managing %s needs root, so re-run with `sudo rec-deploy service %s`", err, daemonUnit, action)
		}

		return err
	}

	if systemd.IsActive(ctx, daemonUnit) {
		ui.Success(daemonUnit + " is active")
	} else {
		ui.Warn(daemonUnit + " is not running — read why with `journalctl -u " + daemonUnit + " -n 50`")
	}

	return nil
}
