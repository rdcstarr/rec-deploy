package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/store"
	"github.com/rdcstarr/rec-deploy/internal/ui"
	"github.com/rdcstarr/rec-deploy/internal/uninstall"
	"github.com/rdcstarr/rec-deploy/internal/units"
)

// unitFiles are every unit the installer drops; the engine removes exactly
// these. It is units.Names and not a copy of it: a fourth unit added there and
// forgotten here would survive uninstall while the report claimed the install was
// gone — a report that lies about what it did is what this command exists not to
// be.
var unitFiles = units.Names

func newUninstallCmd() *cobra.Command {
	var keepGitHub, keepData bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove rec-deploy from this server",
		Long: "uninstall deletes the deploy keys and webhooks on GitHub for every registered\n" +
			"repository, stops and removes the systemd units, deletes the configuration and\n" +
			"state (token, HMAC secrets, deploy keys, database) and removes the binary.\n" +
			"The deployed checkouts on disk are never touched.",
		Example: "rec-deploy uninstall --yes --keep-github",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUninstall(cmd.Context(), keepGitHub, keepData)
		},
	}

	cmd.Flags().BoolVar(&keepGitHub, "keep-github", false, "leave the deploy keys and webhooks on GitHub (and the local records)")
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "leave the configuration and state directories in place")

	return cmd
}

// runUninstall drives the whole removal: inventory, wizard, GitHub cleanup
// with its failure gate, then the local engine, then the report.
func runUninstall(ctx context.Context, keepGitHub, keepData bool) error {
	if os.Geteuid() != 0 {
		dir, _ := config.Dir()
		return fmt.Errorf("uninstall removes system paths — run it as root; a non-root setup is removed with just  rm -rf %s", dir)
	}

	confDir, err := config.Dir()
	if err != nil {
		return err
	}
	stateDir, err := config.StateDir()
	if err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}

	// Inventory before any question: the operator confirms facts, not vibes.
	repos, err := registeredRepos(ctx)
	if err != nil {
		if !keepGitHub {
			return fmt.Errorf("cannot read the registered repositories: %w — fix the store, or accept the github orphans with `--keep-github`", err)
		}
		if !flagJSON {
			ui.Warn("cannot read the registered repositories: " + err.Error())
		}
	}
	if !flagJSON {
		ui.Info(fmt.Sprintf("this removes rec-deploy from this server: %d registered repositories, the systemd units, %s, %s and %s", len(repos), confDir, stateDir, bin))
		ui.Info("the deployed checkouts on disk stay untouched")
	}

	if !flagYes {
		if !isInteractive() {
			return errors.New("uninstall is destructive — re-run with `--yes` (and optionally `--keep-github` / `--keep-data`)")
		}

		ok, err := ui.Confirm("Uninstall rec-deploy from this server?", "")
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		if !keepGitHub && len(repos) > 0 {
			ok, err := ui.Confirm(fmt.Sprintf("Also delete the deploy keys and webhooks on GitHub for the %d registered repositories?", len(repos)),
				"Answering no keeps them on GitHub — pushes keep arriving at a dead endpoint until you clean them up by hand.")
			if err != nil {
				return err
			}
			keepGitHub = !ok
		}
		if !keepData {
			ok, err := ui.Confirm("Delete the local data — GitHub token, HMAC secrets, deploy keys, state database?",
				"Answering no keeps "+confDir+" and "+stateDir+" for a later reinstall.")
			if err != nil {
				return err
			}
			keepData = !ok
		}
	}

	// Phase 1: GitHub cleanup while the token and the stored IDs still exist.
	var cleaned, gone []string
	var failed []string
	if !keepGitHub && len(repos) > 0 {
		client, err := githubClient(ctx)
		if err != nil {
			return fmt.Errorf("github cleanup needs the configured token: %w — re-run with `--keep-github` to skip it", err)
		}
		st, err := openStore(ctx)
		if err != nil {
			return err
		}

		for _, repo := range repos {
			switch err := deleteRepoArtifacts(ctx, st, client, repo); {
			case err == nil:
				cleaned = append(cleaned, repo.Repository)
				if !flagJSON {
					ui.Success("cleaned " + repo.Repository + " on github")
				}
			case errors.Is(err, github.ErrNotFound):
				gone = append(gone, repo.Repository)
				if !flagJSON {
					ui.Info("already gone on github: " + repo.Repository)
				}
			default:
				failed = append(failed, repo.Repository)
				if !flagJSON {
					ui.Warn("github cleanup failed for " + repo.Repository + ": " + err.Error())
				}
			}
		}
		_ = st.Close()

		// The gate: deleting the data below destroys the token and the IDs —
		// after that nobody can finish this cleanup. Stop unless the operator
		// explicitly accepts the orphans.
		if len(failed) > 0 && !keepData {
			if !isInteractive() {
				return fmt.Errorf("github cleanup failed for %d repositories — fix connectivity and re-run, or re-run with `--keep-github`", len(failed))
			}
			ok, err := ui.Confirm(fmt.Sprintf("GitHub cleanup failed for %d repositories — delete the local data anyway?", len(failed)),
				"The keys and webhooks would stay orphaned on GitHub with nothing left here to remove them.")
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("uninstall stopped — nothing local was removed")
			}
		}
	}

	// Phase 2: the local system.
	rep := uninstall.Run(ctx, uninstall.Options{
		UnitsDir:   "/etc/systemd/system",
		Units:      unitFiles,
		DataDirs:   []string{confDir, stateDir, "/usr/local/lib/rec-deploy"},
		BinaryPath: bin,
		KeepData:   keepData,
	})

	if flagJSON {
		return ui.PrintJSON(map[string]any{
			"github": map[string]any{"cleaned": cleaned, "already_gone": gone, "failed": failed, "kept": keepGitHub},
			"report": rep,
		})
	}

	for _, s := range rep.Steps {
		line := s.Target + ": " + string(s.Outcome)
		if s.Detail != "" {
			line += " — " + s.Detail
		}
		switch s.Outcome {
		case uninstall.OutcomeFailed:
			ui.Warn(line)
		default:
			ui.Info(line)
		}
	}

	if rep.Package != "" {
		ui.Info("installed via a package — finish with:  dpkg -r " + rep.Package + "  (or `rpm -e`)")
	}

	if rep.Failed() || len(failed) > 0 {
		return errors.New("uninstall finished with failures — see the lines above; a re-run finishes what is left")
	}

	ui.Success("rec-deploy removed — the deployed checkouts on disk are untouched")

	return nil
}

// registeredRepos lists every registered repository, opening the store, reading
// it and closing it again — so no caller holds a handle across whatever it does
// with the result. An absent state database reads as none: for `uninstall` that
// is a half-removed install with nothing left to clean, and for `pickRepo` it is
// a server where `init` has not run yet. A store that exists and cannot be read
// is still an error, because uninstall deleting the data would orphan every
// deploy key and webhook with nothing left here to remove them.
func registeredRepos(ctx context.Context) ([]store.Repo, error) {
	db, err := config.StateDB()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(db); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	st, err := openStore(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()

	repos, err := st.Repos(ctx)
	if err != nil {
		return nil, err
	}

	return repos, nil
}
