package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/buildinfo"
	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/notify"
	"github.com/rdcstarr/rec-deploy/internal/selfupdate"
	"github.com/rdcstarr/rec-deploy/internal/store"
	"github.com/rdcstarr/rec-deploy/internal/systemd"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// daemonUnit is the systemd unit an unattended update brings back after
// replacing the binary.
const daemonUnit = "rec-deploy.service"

// newSelfUpdateCmd builds the `self-update` command: replace the running binary
// with the latest GitHub release, or just check for one with --check.
func newSelfUpdateCmd() *cobra.Command {
	var check bool
	var restart bool

	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Update rec-deploy to the latest release",
		Long: "Replace the running rec-deploy binary with the latest GitHub release for your OS/architecture, " +
			"after verifying its SHA-256 against the release checksums — a mismatch aborts the update. " +
			"It installs with sudo when rec-deploy lives in a root-owned path. Releases are public, so no token is required.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			current := buildinfo.Resolved()

			if restart {
				return selfUpdateRestart(cmd.Context(), current)
			}

			if check {
				return selfUpdateCheck(cmd.Context(), current)
			}

			if isInteractive() && !flagJSON {
				return selfUpdateInteractive(cmd.Context(), current)
			}

			return selfUpdateInstall(cmd.Context(), current)
		},
	}

	cmd.Flags().BoolVar(&check, "check", false, "only check whether a newer release is available")
	cmd.Flags().BoolVar(&restart, "restart", false,
		"after updating, restart rec-deploy.service and roll back if it does not stay up (for rec-deploy-update.service)")

	return cmd
}

// selfUpdateInteractive is the whole interactive update: check, and if there is
// something to install, say what and ask. It replaces a menu whose second entry
// was the only thing the first entry could lead to.
func selfUpdateInteractive(ctx context.Context, current string) error {
	var res selfupdate.Result
	if err := ui.Spinner("Checking for updates…", func() error {
		var err error
		res, err = selfupdate.Check(ctx, current)

		return err
	}); err != nil {
		return err
	}

	if !res.Newer {
		ui.Success("rec-deploy is up to date (" + res.Current + ")")

		return ui.ErrBack
	}

	ok, err := ui.Confirm("Install "+res.Latest+"?", "rec-deploy "+res.Current+" → "+res.Latest+". The download is verified against the release checksums; a mismatch aborts the update.")
	if err != nil {
		return err
	}
	if !ok {
		return ui.ErrBack
	}

	wantRestart, err := confirmRestart(ctx, current, res.Latest)
	if err != nil {
		return err
	}

	return runUpdatePath(wantRestart,
		func() error { return selfUpdateInstall(ctx, current) },
		func() error {
			return ui.Spinner("Updating rec-deploy and restarting "+daemonUnit+"…", func() error {
				return selfUpdateRestart(ctx, current)
			})
		},
	)
}

// runUpdatePath performs exactly one of install or restart, never both. It
// exists so the routing decision itself is a single, unit-testable function:
// selfUpdateInteractive used to call selfUpdateInstall unconditionally and
// then, on a confirmed restart, ApplyAndRestart a second time — which backed
// up the binary the first install had already replaced, so a rollback
// restored a copy of the new, possibly broken, release instead of the
// genuine outgoing one. Keeping the two paths mutually exclusive here is
// what makes that bug impossible to reintroduce silently.
func runUpdatePath(wantRestart bool, install, restart func() error) error {
	if wantRestart {
		return restart()
	}

	return install()
}

// confirmRestart asks whether to bring the daemon onto the release that is
// about to be installed, using the supervised restart path (backup, restart,
// three consecutive health samples, automatic rollback) rather than a bare
// restart — an operator restarting by hand deserves the same net as one who
// never watches it happen.
//
// It only decides; it does not install or restart anything itself. Asking
// before anything is written is what lets the caller take exactly one path
// afterward instead of installing and then, separately, restarting.
func confirmRestart(ctx context.Context, current, latest string) (bool, error) {
	if !systemd.Available() {
		ui.Info("restart the daemon to run the new version")

		return false, nil
	}
	if !systemd.IsActive(ctx, daemonUnit) {
		ui.Info(daemonUnit + " is not running and was left stopped — start it to run the new version")

		return false, nil
	}

	detail := "The daemon is still running " + current + ". It is stopped and started on " + latest + ", and rolled back automatically if it does not stay up."
	if running, err := runningDeployCount(ctx); err != nil {
		slog.Warn("cannot count running deploys for the restart prompt", "error", err)
	} else if running > 0 {
		detail = plural(running, "deploy") + " running right now would be cut short. " + detail
	}

	ok, err := ui.Confirm("Restart "+daemonUnit+" now?", detail)
	if err != nil {
		return false, err
	}
	if !ok {
		ui.Info("restart it when convenient:  systemctl restart " + daemonUnit)
	}

	return ok, nil
}

// runningDeployCount is how many deploys are in flight, so the restart prompt
// can say what it would interrupt.
func runningDeployCount(ctx context.Context) (int, error) {
	st, err := openStore(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = st.Close() }()

	deploys, err := st.Deploys(ctx, "", 50)
	if err != nil {
		return 0, err
	}

	running := 0
	for _, d := range deploys {
		if d.Status == store.StatusRunning {
			running++
		}
	}

	return running, nil
}

func selfUpdateCheck(ctx context.Context, current string) error {
	var res selfupdate.Result
	err := ui.Spinner("Checking for updates…", func() error {
		var err error
		res, err = selfupdate.Check(ctx, current)

		return err
	})
	if err != nil {
		return err
	}
	if flagJSON {
		return ui.PrintJSON(res)
	}
	if res.Newer {
		ui.Info(fmt.Sprintf("update available: %s → %s", res.Current, res.Latest))
	} else {
		ui.Success("rec-deploy is up to date (" + res.Current + ")")
	}

	return nil
}

func selfUpdateInstall(ctx context.Context, current string) error {
	// Prepare does all network I/O (check, download and checksum verification),
	// so it runs under a spinner. Install stays outside because sudo may prompt.
	var update *selfupdate.Update
	err := ui.Spinner("Downloading update…", func() error {
		var err error
		update, err = selfupdate.Prepare(ctx, current)

		return err
	})
	if err != nil {
		return err
	}

	res, err := update.Install(ctx)
	if err != nil {
		return err
	}
	if flagJSON {
		return ui.PrintJSON(res)
	}
	if res.Updated {
		ui.Success(fmt.Sprintf("updated rec-deploy %s → %s", res.Current, res.Latest))
		ui.Info("re-run rec-deploy to use the new version")
	} else {
		ui.Success("rec-deploy is already up to date (" + res.Current + ")")
	}

	return nil
}

// skipsKnownBadRelease reports whether the newest release is one the updater has
// already watched crash. It skips only when the newest release is exactly the
// recorded bad tag: a newer release (a fix) always has a different tag and must
// install normally.
func skipsKnownBadRelease(badTag string, chk selfupdate.Result) bool {
	return badTag != "" && chk.Newer && chk.Latest == badTag
}

// selfUpdateRestart is the unattended path rec-deploy-update.service runs on a
// timer. It writes to the journal, not to a terminal, so there is no spinner —
// and it notifies, because the operator has to learn that a release was rolled
// back on a machine they did not know had a problem.
func selfUpdateRestart(ctx context.Context, current string) error {
	if !systemd.Available() {
		return errors.New("--restart needs systemd — run `rec-deploy self-update` and restart the service yourself")
	}

	stateDir, err := config.StateDir()
	if err != nil {
		return err
	}

	cfg := Config()

	// The bad-release memory is best-effort: it suppresses a retry, it does not
	// gate the update. A store failure degrades to the pre-memory behaviour
	// (install, and roll back if the release is bad) — never to blocking updates.
	var st *store.Store
	if s, err := openStore(ctx); err != nil {
		slog.Warn("cannot open the state store for bad-release memory; proceeding without it", "error", err)
	} else {
		st = s
		defer func() { _ = st.Close() }()
	}

	if st != nil {
		if badTag, err := st.LastBadTag(ctx); err != nil {
			slog.Warn("cannot read the last bad release; proceeding without the skip", "error", err)
		} else if badTag != "" {
			// A pushed tag is skipped only if it is still the newest release; a
			// Check failure here just means we fall through and let ApplyAndRestart
			// decide, so its error is not fatal either.
			if chk, err := selfupdate.Check(ctx, current); err == nil && skipsKnownBadRelease(badTag, chk) {
				slog.Info("skipping a release that already failed to start after an update", "tag", badTag)

				return nil
			}
		}
	}

	res, err := selfupdate.ApplyAndRestart(ctx, current, selfupdate.RestartOptions{
		Unit:       daemonUnit,
		BackupPath: filepath.Join(stateDir, "rec-deploy.prev"),
		Wait:       30 * time.Second,
	})

	if res.RolledBack && st != nil {
		// Remembering the bad tag is exactly what stops it being retried, so it
		// must survive a ctx that a shutdown cancelled — the same reason rollback
		// detaches its recovery. Without this, an operator stopping the update
		// service during the health window would cancel ctx, skip the record, and
		// let the bad release come back next tick.
		rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if err := st.RecordBadTag(rec, res.Latest); err != nil {
			slog.Error("could not remember the bad release; it may be retried next tick", "tag", res.Latest, "error", err)
		}
		cancel()
	}

	if res.RolledBack {
		// Do not assert a fixed outcome here. RolledBack is set at the top of
		// rollback, before restore and the recovery restart run, so it is true across
		// three different real states: the daemon is safely back on Current, the
		// restore itself failed, or the binary was restored but the unit would not
		// restart. err carries the authoritative outcome (one of rollback's three
		// messages), so lead with an outcome-neutral summary and let err state exactly
		// what happened rather than prefixing it with a claim it may contradict.
		var detail string
		if err != nil {
			detail = err.Error()
		}
		notify.SendUpdate(ctx, cfg.Notify, notify.UpdateSummary{
			From:    res.Current,
			To:      res.Latest,
			Outcome: notify.RolledBack,
			Detail:  detail,
		})
	}
	if err != nil {
		return err
	}

	if !res.Updated {
		slog.Info("rec-deploy is up to date", "version", res.Current)

		return nil
	}

	// Updated alone does not say whether the daemon is now running the new
	// release: superviseRestart installs the binary but deliberately skips the
	// restart when the operator had already stopped the unit. Restarted is the
	// field that tells the two apart — without branching on it, an operator who
	// stopped the service for maintenance would be told it was restarted while it
	// stays down.
	if !res.Restarted {
		notify.SendUpdate(ctx, cfg.Notify, notify.UpdateSummary{
			From:    res.Current,
			To:      res.Latest,
			Unit:    daemonUnit,
			Outcome: notify.Installed,
		})

		if flagJSON {
			return ui.PrintJSON(res)
		}

		ui.Success(fmt.Sprintf("installed rec-deploy %s → %s; %s was not running and was left stopped — start it when ready",
			res.Current, res.Latest, daemonUnit))

		return nil
	}

	notify.SendUpdate(ctx, cfg.Notify, notify.UpdateSummary{
		From:    res.Current,
		To:      res.Latest,
		Unit:    daemonUnit,
		Outcome: notify.Updated,
	})

	if flagJSON {
		return ui.PrintJSON(res)
	}

	ui.Success(fmt.Sprintf("updated rec-deploy %s → %s and restarted %s", res.Current, res.Latest, daemonUnit))

	return nil
}
