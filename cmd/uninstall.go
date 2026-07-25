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
	var keepGitHub, keepData, keepCloudflare bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove rec-deploy from this server",
		Long: "uninstall deletes the deploy keys and webhooks on GitHub for every registered\n" +
			"repository, deletes the Cloudflare tunnel and hostname behind remote MCP, stops\n" +
			"and removes the systemd units, deletes the configuration and state (token, HMAC\n" +
			"secrets, deploy keys, database) and removes the binary.\n" +
			"The deployed checkouts on disk are never touched.",
		Example: "rec-deploy uninstall --yes --keep-github",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUninstall(cmd.Context(), keepGitHub, keepCloudflare, keepData)
		},
	}

	cmd.Flags().BoolVar(&keepGitHub, "keep-github", false, "leave the deploy keys and webhooks on GitHub (and the local records)")
	cmd.Flags().BoolVar(&keepCloudflare, "keep-cloudflare", false, "leave the remote MCP tunnel and hostname in the Cloudflare account")
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "leave the configuration and state directories in place")

	return cmd
}

// runUninstall drives the whole removal: inventory, wizard, GitHub cleanup
// with its failure gate, then the local engine, then the report.
func runUninstall(ctx context.Context, keepGitHub, keepCloudflare, keepData bool) error {
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

	if !flagJSON {
		ui.Title("rec-deploy uninstall")
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
	// Read before any question, like the repository inventory: the operator is
	// told what exists, not asked about a resource that may not.
	hasCloudflare := cloudflareMCPProvisioned(Config())

	if !flagJSON {
		ui.Info(fmt.Sprintf("this removes rec-deploy from this server: %d registered repositories, the systemd units, %s, %s and %s", len(repos), confDir, stateDir, bin))
		if hasCloudflare {
			ui.Info("and the Cloudflare tunnel and hostname behind remote MCP: " + Config().MCP.Cloudflare.Hostname)
		}
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
			// Declining the whole uninstall delivered nothing: nil would read to
			// dispatch as a finished command and unwind past the hub.
			return ui.ErrBack
		}

		if !keepGitHub && len(repos) > 0 {
			ok, err := ui.Confirm(fmt.Sprintf("Also delete the deploy keys and webhooks on GitHub for the %d registered repositories?", len(repos)),
				"Answering no keeps them on GitHub — pushes keep arriving at a dead endpoint until you clean them up by hand.")
			if err != nil {
				return err
			}
			keepGitHub = !ok
		}
		if !keepCloudflare && hasCloudflare {
			ok, err := ui.Confirm("Also delete the Cloudflare tunnel and hostname behind remote MCP?",
				"Answering no leaves "+Config().MCP.Cloudflare.Hostname+" and its tunnel in your Cloudflare account, with the credentials to remove them about to be erased from this server.")
			if err != nil {
				return err
			}
			keepCloudflare = !ok
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

	// The confirms are what decide which phases the run will take, so the
	// numbering is only final here — the same rule ui.RunWizard's Skip enforces
	// for init, where numbering a step the operator never reaches would promise
	// work that does not happen.
	doGitHub := !keepGitHub && len(repos) > 0
	doCloudflare := !keepCloudflare && hasCloudflare
	steps := stepper{total: 1 + boolToInt(doGitHub) + boolToInt(doCloudflare)}

	// Phase 1: GitHub cleanup while the token and the stored IDs still exist.
	var cleaned, gone []string
	var failed []string
	if doGitHub {
		steps.next("GitHub")

		client, err := githubClient(ctx)
		if err != nil {
			return fmt.Errorf("github cleanup needs the configured token: %w — re-run with `--keep-github` to skip it", err)
		}
		st, err := openStore(ctx)
		if err != nil {
			return err
		}

		for _, repo := range repos {
			// Two GitHub API calls per repository — show which one is being
			// cleaned rather than freeze on a long list of them.
			err := ui.Spinner("Cleaning "+repo.Repository+" on github…", func() error {
				return deleteRepoArtifacts(ctx, st, client, repo)
			})
			switch {
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
	}

	// Phase 1b: Cloudflare, still before the data goes — the API token and the
	// tunnel's own ID both live in the config about to be deleted, and a tunnel
	// nobody can name is a tunnel nobody deletes.
	cloudflareRemoved := false
	var cloudflareErr error
	if doCloudflare {
		steps.next("Remote MCP")

		cloudflareErr = removeCloudflareMCP(ctx)
		switch {
		case cloudflareErr == nil:
			cloudflareRemoved = true
			if !flagJSON {
				ui.Success("deleted the Cloudflare tunnel and hostname")
			}
		case ui.IsQuit(cloudflareErr) || errors.Is(cloudflareErr, ui.ErrBack):
			// Ctrl+C or Esc at the credentials prompt is the operator leaving, not
			// a cleanup failure. Both stop the uninstall: recording either as a
			// failure printed the navigation sentinel ("rec-deploy: back one
			// level") into a warning and — with the data kept, so the gate fired
			// on a deliberate back-out — offered to remove the whole install
			// anyway, which a numbered step would make read as sanctioned. The
			// deliberate skip is a different gesture: an empty token yields the
			// actionable "no Cloudflare token given" error, which still flows to
			// the gate below.
			return cloudflareErr
		case !flagJSON:
			ui.Warn("cloudflare cleanup failed: " + cloudflareErr.Error())
		}
	}

	// The gate: deleting the data below destroys the GitHub token, the stored
	// IDs and the Cloudflare credentials — after that nobody can finish either
	// cleanup. Stop unless the operator explicitly accepts the orphans.
	if orphans := len(failed) + boolToInt(cloudflareErr != nil); orphans > 0 && !keepData {
		if !isInteractive() {
			return fmt.Errorf("remote cleanup failed for %d resources — fix connectivity and re-run, or re-run with `--keep-github` / `--keep-cloudflare`", orphans)
		}
		// Everything but yes means no here — Esc and Ctrl+C included. There is no
		// level to back out to once the remote phases have run, and both signals
		// are clean exits: returning them ended a half-uninstalled server at
		// status 0 with nothing said about the orphans, while answering "no" to
		// the very same question exited 1. This is a deliberate exception to the
		// global navigation contract, at the one prompt that runs after
		// destruction has already started.
		ok, err := ui.Confirm(fmt.Sprintf("Remote cleanup failed for %d resources — delete the local data anyway?", orphans),
			"They would stay orphaned on GitHub or in Cloudflare with nothing left here to remove them.")
		if err != nil || !ok {
			return errors.New("uninstall stopped — nothing local was removed")
		}
	}

	// Phase 2: the local system.
	steps.next("Local system")

	// Run reports rather than returns: it never aborts midway, so a half-removed
	// install stays finishable by a second run. It shells out to systemctl up to
	// fifteen times and rec-deploy.service drains in-flight deploys before it
	// stops, so a heading followed by a blank screen is not an option.
	var rep uninstall.Report
	_ = ui.Spinner("Stopping the services and removing the files…", func() error {
		rep = uninstall.Run(ctx, uninstall.Options{
			UnitsDir:   "/etc/systemd/system",
			Units:      unitFiles,
			DataDirs:   []string{confDir, stateDir, "/usr/local/lib/rec-deploy"},
			BinaryPath: bin,
			KeepData:   keepData,
		})

		return nil
	})

	if flagJSON {
		return ui.PrintJSON(map[string]any{
			"github":     map[string]any{"cleaned": cleaned, "already_gone": gone, "failed": failed, "kept": keepGitHub},
			"cloudflare": map[string]any{"provisioned": hasCloudflare, "removed": cloudflareRemoved, "kept": keepCloudflare, "error": errorText(cloudflareErr)},
			"report":     rep,
		})
	}

	for _, line := range reportLines(rep) {
		ui.Out(line)
	}

	if rep.Package != "" {
		ui.Info("installed via a package — finish with:  dpkg -r " + rep.Package + "  (or `rpm -e`)")
	}

	if rep.Failed() || len(failed) > 0 || cloudflareErr != nil {
		return errors.New("uninstall finished with failures — see the lines above; a re-run finishes what is left")
	}

	ui.Out("")
	ui.Success("rec-deploy removed — the deployed checkouts on disk are untouched")

	return nil
}

// stepper prints the "[n/total] Name" headings ui.RunWizard prints for init.
// uninstall renders them itself rather than running a wizard, for two reasons:
// ui.Step writes to stdout unconditionally, and under --json stdout carries
// exactly one JSON document; and the orphan gate has to sit between two steps
// rather than inside one, while a wizard prints a step's heading before its
// body — so a gate that aborted there would announce a phase that never ran.
type stepper struct {
	n     int
	total int
}

// next announces the step about to run.
func (s *stepper) next(name string) {
	s.n++
	if !flagJSON {
		ui.Step(s.n, s.total, name)
	}
}

// reportLines renders the engine's report the way the local step prints it:
// each phase introduced by its own label, then that phase's targets verbatim
// and in engine order, indented under it.
//
// Nothing is summarised — on a destructive run "7 unit files removed" hides
// which targets were actually touched, and package ownership is asked per file,
// so the seven are not uniform anyway. Nothing is dropped either: a step whose
// phase the engine did not name still prints, because a report that omits what
// it did is the defect this command exists not to be.
func reportLines(rep uninstall.Report) []string {
	lines := make([]string, 0, len(rep.Steps)+4)
	phase := uninstall.Phase("")
	for _, s := range rep.Steps {
		if s.Phase != phase {
			phase = s.Phase
			lines = append(lines, ui.Dim("  "+string(phase)))
		}

		line := s.Target + ": " + string(s.Outcome)
		if s.Detail != "" {
			line += " — " + s.Detail
		}
		if s.Outcome == uninstall.OutcomeFailed {
			// The marker and its space take two of the four columns, so a
			// failure keeps its "!" without stepping right of the lines above it.
			lines = append(lines, ui.Alert("!")+"   "+line)

			continue
		}
		lines = append(lines, ui.Dim("    "+line))
	}

	return lines
}

// removeCloudflareMCP deletes the tunnel and the hostname behind remote MCP. It
// takes the connector down first: Cloudflare refuses to delete a tunnel that
// still has a live connection, and uninstall.Run only stops the units later.
func removeCloudflareMCP(ctx context.Context) error {
	cf := Config().MCP.Cloudflare

	client, err := cloudflareCleanupClient(ctx, cf)
	if err != nil {
		return err
	}

	// Stopping the connector is part of the delete and blocks while cloudflared
	// drains, so it runs inside the spinner rather than as a dead pause before it.
	return ui.Spinner("Deleting the Cloudflare tunnel and hostname…", func() error {
		_ = stopMCPTunnel(ctx)

		return deleteCloudflareMCP(ctx, client, cf)
	})
}

// boolToInt counts a condition, so the orphan gate can add a single Cloudflare
// failure to a list of failed repositories without two near-identical branches.
func boolToInt(b bool) int {
	if b {
		return 1
	}

	return 0
}

// errorText renders err for --json, where a nil error must read as absent
// rather than as the string "nil".
func errorText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
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
