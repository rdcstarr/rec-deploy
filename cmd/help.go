package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// menuHelp is the help shown when h is pressed in an interactive menu. It is set
// in the root PersistentPreRunE to the running command's help, so every menu —
// including nested ones — shows the right help without threading the command
// through each call.
var menuHelp string

// selectMenu shows an interactive single-choice menu whose h key reveals the
// current command's help (commandHelp via menuHelp). Going back (Esc / ←)
// returns "", which callers treat as "up one menu level"; quitting with q or
// Ctrl+C returns ui.ErrQuit, which unwinds the whole session.
func selectMenu(title string, options []ui.Option) (string, error) {
	res, err := ui.Picker{Title: title, Options: options, Help: menuHelp}.Run()

	return res.Value, err
}

// commandHelp renders a styled help block for cmd — its subcommands, flags and
// examples — shown in interactive menus when help is toggled with h.
func commandHelp(cmd *cobra.Command) string {
	var b strings.Builder

	var subs [][2]string
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() && c.Name() != "help" {
			subs = append(subs, [2]string{c.Name(), c.Short})
		}
	}
	if len(subs) > 0 {
		b.WriteString(ui.Heading("Commands") + "\n")
		b.WriteString(ui.TwoCol(subs))
	}

	var flags [][2]string
	add := func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		name := "--" + f.Name
		if f.Shorthand != "" {
			name = "-" + f.Shorthand + ", " + name
		}
		if t := f.Value.Type(); t != "bool" {
			name += " " + t
		}
		usage := f.Usage
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && f.DefValue != "[]" {
			usage += " (default " + f.DefValue + ")"
		}
		flags = append(flags, [2]string{name, usage})
	}
	cmd.LocalFlags().VisitAll(add)
	cmd.InheritedFlags().VisitAll(add)
	if len(flags) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(ui.Heading("Flags") + "\n")
		b.WriteString(ui.TwoCol(flags))
	}

	if ex := strings.TrimSpace(cmd.Example); ex != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(ui.Heading("Examples") + "\n")
		for _, line := range strings.Split(ex, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				b.WriteString("  " + ui.Dim(line) + "\n")
			}
		}
	}

	return strings.TrimRight(b.String(), "\n")
}
