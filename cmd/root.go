// Package cmd wires the rec-deploy cobra command tree. Commands stay thin: they
// parse and validate flags, then delegate to the internal packages.
package cmd

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/buildinfo"
	"github.com/rdcstarr/rec-deploy/internal/cli"
	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

var (
	flagConfig  string
	flagNoColor bool
	flagVerbose bool
	flagJSON    bool
	flagYes     bool

	// cfg holds the configuration loaded for the current command run.
	cfg *config.Config
)

const annotationInteractive = "rec-deploy.io/interactive"

// Execute builds the command tree and runs it with the given context. The
// interactive navigation signals — back (ui.ErrBack) and quit (ui.ErrQuit) — are
// clean exits, not errors. Backing out of a command launched directly (e.g.
// `rec-deploy repo`, `rec-deploy status`) has no hub above it to climb to, so open the
// rec-deploy hub here — the same level a back-out lands on when the command was
// chosen from the hub. ← therefore always climbs toward the hub regardless of
// entry point.
func Execute(ctx context.Context) error {
	root := newRootCmd()
	err := root.ExecuteContext(ctx)
	if errors.Is(err, ui.ErrBack) && isInteractive() {
		err = rootMenu(root)
	}
	if err == nil || ui.IsQuit(err) || errors.Is(err, ui.ErrBack) {
		return nil
	}

	return err
}

// Config returns the configuration loaded during PersistentPreRunE, available
// to subcommands after flag parsing.
func Config() *config.Config {
	return cfg
}

// newRootCmd builds the root command, registers global flags, and attaches
// every subcommand.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "rec-deploy",
		Short:         "Deploy GitHub repositories in place",
		Long:          "rec-deploy deploys GitHub repositories in place on this server and administers their deploy keys and webhooks.",
		Version:       buildinfo.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			ui.SetColor(!flagNoColor && !envNoColor())
			logLevel := slog.LevelWarn
			if flagVerbose {
				logLevel = slog.LevelDebug
			}
			cli.SetupLogger(logLevel)
			ui.ResetQuit()

			loaded, err := config.Load(flagConfig)
			if err != nil {
				return err
			}
			cfg = loaded

			// Help shown when h is pressed in any interactive menu of this command.
			menuHelp = commandHelp(cmd)

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 || !isInteractive() {
				return cmd.Help()
			}

			return rootMenu(cmd)
		},
	}

	root.PersistentFlags().StringVar(&flagConfig, "config", "", "config file (default /etc/rec-deploy/config.yaml as root, ~/.config/rec-deploy/config.yaml otherwise)")
	root.PersistentFlags().BoolVar(&flagNoColor, "no-color", false, "disable colored output")
	root.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "enable verbose diagnostic logging")
	root.PersistentFlags().BoolVar(&flagJSON, "json", false, "emit JSON output where supported")
	root.PersistentFlags().BoolVar(&flagYes, "yes", false, "assume yes for destructive confirmations")

	root.AddCommand(newInitCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newRepoCmd())
	root.AddCommand(newDeployCmd())
	root.AddCommand(newRollbackCmd())
	root.AddCommand(newScanCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newNotifyCmd())
	root.AddCommand(newServeCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newSelfUpdateCmd())
	root.AddCommand(newCompletionCmd())
	root.AddCommand(newUninstallCmd())
	root.AddCommand(newMCPCmd())

	return root
}

// envNoColor reports whether the NO_COLOR environment variable is set (any
// value), per the no-color.org convention.
func envNoColor() bool {
	_, ok := os.LookupEnv("NO_COLOR")
	return ok
}

// isInteractive reports whether stdin is a terminal, i.e. whether the user can
// answer a prompt. Piped, CI and systemd runs are not interactive.
//
// The check is an isatty ioctl rather than os.Stdin.Stat()'s ModeCharDevice bit:
// /dev/null is itself a character device, so the Stat form calls systemd — whose
// default StandardInput=null — interactive, and the root menu would then try to
// open a TTY that is not there.
func isInteractive() bool {
	return isatty.IsTerminal(os.Stdin.Fd())
}

// rootMenu runs the top-level command hub (rec-deploy with no arguments): it loops,
// showing an interactive command picker and running the chosen command, then
// returns here when that command finishes. A command that requires arguments is
// prompted for them first. Backing out of the picker (←/esc) exits to the shell.
func rootMenu(cmd *cobra.Command) error {
	ui.Banner(buildinfo.Resolved())

	options := hubOptions(cmd)

	for {
		if ui.Quitting() {
			return ui.ErrQuit
		}

		// h in the root menu must show the root's help; each dispatched
		// subcommand's PersistentPreRunE overwrites menuHelp, so reset it here.
		menuHelp = commandHelp(cmd)

		choice, err := selectMenu("rec-deploy — choose a command", options)
		if err != nil {
			return err
		}
		if choice == "" {
			return nil
		}

		switch err := dispatch(cmd, choice); {
		case ui.IsQuit(err):
			return err
		case errors.Is(err, ui.ErrBack):
			// Backed out of the command (a group's menu, or a prompt) — re-show
			// the hub.
		case err != nil:
			ui.RenderError(err) // real error — re-show the hub so they can retry
		default:
			// A one-shot command completed — exit to the shell with its output
			// in view instead of re-showing the menu.
			return nil
		}
	}
}

// hubOptions builds the command hub's option list: every browsable subcommand,
// labelled with its name padded to a fixed column and a dimmed Short. help and
// completion are cobra's own plumbing rather than operator commands, so they are
// left out, as are Hidden commands. uninstall is intentionally included — its
// root check and confirmation wizard are what guard it, not its absence here.
func hubOptions(cmd *cobra.Command) []ui.Option {
	var visible []*cobra.Command
	width := 0
	for _, c := range cmd.Commands() {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" || c.Annotations[annotationInteractive] == "false" {
			continue
		}

		visible = append(visible, c)
		if n := len(c.Name()); n > width {
			width = n
		}
	}

	options := make([]ui.Option, 0, len(visible))
	for _, c := range visible {
		label := c.Name()
		if c.Short != "" {
			// Pad the name to a fixed column so descriptions align, and dim the
			// description so the command name stands out.
			label = c.Name() + strings.Repeat(" ", width-len(c.Name())) + "   " + ui.Dim(c.Short)
		}

		options = append(options, ui.Option{Label: label, Value: c.Name()})
	}

	return options
}

// dispatch runs the subcommand named name of cmd, as chosen from an interactive
// menu: it prompts for the positional arguments the command requires but does
// not have, then re-executes the tree from the root so the command runs through
// cobra's full dispatch (PersistentPreRunE, flag parsing) rather than a bare
// RunE call. Reusing the same root preserves the global flags (--no-color,
// --json) across the loop. Backing out of the prompt yields ui.ErrBack, which
// every menu loop reads as "re-show me".
//
// A command that merely *accepts* an argument validates with none and is
// therefore never prompted here: it asks for what it needs itself, through
// interactiveArg or pickRepo, so it can offer a picker instead of a blank line.
func dispatch(cmd *cobra.Command, name string) error {
	root := cmd.Root()

	// The path from the root down to the chosen command, minus the binary name:
	// "rec-deploy repo" + "add" → ["repo", "add"].
	args := append(strings.Fields(cmd.CommandPath())[1:], name)

	target, _, err := root.Find(args)
	if err != nil {
		return err
	}

	if target.ValidateArgs(nil) != nil {
		input, err := ui.Prompt(target.Use, "", "")
		if err != nil {
			return err // ui.ErrBack (re-show the menu) or ui.ErrQuit (quit)
		}
		if input = strings.TrimSpace(input); input == "" {
			return ui.ErrBack
		}

		args = append(args, strings.Fields(input)...)
	}

	root.SetArgs(args)

	return root.ExecuteContext(cmd.Context())
}

// interactiveArg resolves the single positional argument a command needs:
// args[0] when given, otherwise an interactive prompt titled title. err carries
// the navigation signal so a command launched from a hub returns to the menu
// (ui.ErrBack) or unwinds the whole session (ui.ErrQuit) instead of looking like
// a successful no-op: backing out of, or entering nothing at, the prompt yields
// ui.ErrBack. ok is true only when a usable value is present; it is false with a
// nil err only for a non-interactive run with no arg — the caller shows help.
func interactiveArg(args []string, title string) (value string, ok bool, err error) {
	if len(args) > 0 {
		return args[0], true, nil
	}
	if !isInteractive() {
		return "", false, nil
	}

	v, err := ui.Prompt(title, "", "")
	if err != nil {
		return "", false, err // ui.ErrBack (re-show hub) or ui.ErrQuit (quit)
	}
	if v = strings.TrimSpace(v); v == "" {
		return "", false, ui.ErrBack // entered nothing — step back
	}

	return v, true, nil
}
