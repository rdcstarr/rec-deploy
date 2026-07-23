package cmd

import (
	"runtime"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/buildinfo"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newVersionCmd builds the `version` subcommand: the rich, scriptable
// build-info view (Cobra's --version gives the one-line form).
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagJSON {
				return ui.PrintJSON(map[string]string{
					"version": buildinfo.Resolved(),
					"commit":  buildinfo.Commit,
					"date":    buildinfo.Date,
					"go":      runtime.Version(),
					"os":      runtime.GOOS,
					"arch":    runtime.GOARCH,
				})
			}

			(ui.Report{Title: "rec-deploy " + buildinfo.Resolved(), Rows: [][2]string{
				{"commit", buildinfo.Commit},
				{"built", buildinfo.Date},
				{"go", runtime.Version()},
				{"platform", runtime.GOOS + "/" + runtime.GOARCH},
			}}).Print()

			return nil
		},
	}
}
