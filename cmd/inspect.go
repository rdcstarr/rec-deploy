package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/deploy"
	"github.com/rdcstarr/rec-deploy/internal/discover"
	"github.com/rdcstarr/rec-deploy/internal/store"
	"github.com/rdcstarr/rec-deploy/internal/systemd"
	"github.com/rdcstarr/rec-deploy/internal/ui"
	"github.com/rdcstarr/rec-deploy/internal/units"
)

// newScanCmd builds `scan`: run discovery and show everything it finds.
func newScanCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "scan",
		Short:   "Show every checkout discovery finds",
		Long:    "scan walks the configured discovery roots and prints every checkout carrying a .rec-deploy.yml — the broken ones included, each flagged with what is wrong with it.",
		Args:    cobra.NoArgs,
		Example: "rec-deploy scan\nrec-deploy scan --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			found, err := scanInstallations(cmd.Context())
			if err != nil {
				return err
			}

			if flagJSON {
				out := make([]map[string]any, 0, len(found))
				for _, in := range found {
					row := installJSON(in)
					row["repository"] = in.Repository
					out = append(out, row)
				}

				return ui.PrintJSON(out)
			}

			renderScan(found)

			return nil
		},
	}
}

// renderScan prints one line per installation. It shows what discovery found,
// not what it approves of: a checkout that will not deploy is listed with the
// reason, never omitted — an installation missing from the output is the one
// question `rec-deploy scan` exists to answer.
func renderScan(found []discover.Installation) {
	if len(found) == 0 {
		ui.Warn("no installation found — check the roots with `rec-deploy config get discovery.roots`")

		return
	}

	rows := make([][2]string, 0, len(found))
	for _, in := range found {
		rows = append(rows, scanRow(in))
	}

	ui.Title(plural(len(found), "installation"))
	ui.Out(ui.TwoCol(rows))
}

// scanRow renders one checkout: its path against its repository, branch, owner
// and every marker that applies — ⚠ root (push access here is root on this
// server), ⚠ https (an origin the deploy key cannot authenticate), ⚠ mixed
// (naming the stray file), and ✗ with the error of a manifest that will not
// parse or an origin that contradicts it.
func scanRow(in discover.Installation) [2]string {
	desc := installFlags(in)
	if in.Repository != "" {
		desc = append([]string{in.Repository}, desc...)
	}

	return [2]string{in.Path, strings.Join(desc, "  ")}
}

// newStatusCmd builds `status`: is the daemon up, what is registered, and what
// did each checkout last do.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Show daemon health, repositories and the last deploy per path",
		Long:    "status probes the webhook daemon, lists the registered repositories, and shows the last deploy recorded for every checkout.",
		Args:    cobra.NoArgs,
		Example: "rec-deploy status\nrec-deploy status --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := showStatus(cmd.Context()); err != nil {
				return err
			}

			// The report is the whole answer for a script; the actions below it
			// only make sense to someone who can pick one.
			if !isInteractive() || flagJSON {
				return nil
			}

			ui.Out("")

			return statusMenu(cmd)
		},
	}
}

// showStatus renders the three things an operator asks for at once: whether the
// daemon is answering, which repositories are registered, and where each
// checkout stands.
func showStatus(ctx context.Context) error {
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	repos, err := st.Repos(ctx)
	if err != nil {
		return err
	}

	paths, err := st.LastDeployPerPath(ctx)
	if err != nil {
		return err
	}

	// The probe blocks for up to two seconds and prints nothing of its own.
	url := healthURL(Config())
	var up bool
	if err := ui.Spinner("Probing the daemon…", func() error {
		up = daemonUp(ctx, url)

		return nil
	}); err != nil {
		return err
	}

	if flagJSON {
		return ui.PrintJSON(map[string]any{
			"daemon":       map[string]any{"url": url, "healthy": up},
			"auto_update":  systemd.IsEnabled(ctx, updateTimer),
			"units":        unitStates(ctx),
			"repositories": repoStates(repos),
			"paths":        pathStates(paths),
		})
	}

	autoUpdate := systemd.IsEnabled(ctx, updateTimer)
	states := unitStates(ctx)
	ui.Title(ui.ScreenPath("rec-deploy", "Status"))
	renderStatusOverview(up, url, systemd.Available(), autoUpdate, states, repos, paths)
	ui.Out("")

	renderRepoStates(repos)
	ui.Out("")
	renderPathStates(paths)

	return nil
}

// renderStatusOverview puts actionable failures before healthy facts so an
// operator can answer "what needs attention?" without scanning every detail.
func renderStatusOverview(up bool, url string, systemdAvailable, autoUpdate bool, states []units.Status, repos []store.Repo, paths []store.DeployPath) {
	var issues, healthy []string
	if up {
		healthy = append(healthy, "daemon answering at "+url)
	} else {
		issues = append(issues, "daemon not answering at "+url+" — start it with `systemctl start rec-deploy`")
	}

	if systemdAvailable {
		if autoUpdate {
			healthy = append(healthy, "auto-update enabled")
		} else {
			healthy = append(healthy, "auto-update disabled (optional)")
		}
	}
	for _, state := range states {
		switch state.State {
		case units.StateCurrent:
			healthy = append(healthy, state.Unit+" current")
		case units.StateStale:
			issues = append(issues, state.Unit+" differs from this version — re-run the installer")
		case units.StateMissing:
			issues = append(issues, state.Unit+" is not installed — re-run the installer")
		case units.StateMasked:
			issues = append(issues, state.Unit+" is masked — run `systemctl unmask "+state.Unit+"`")
		case units.StateUnreadable:
			issues = append(issues, state.Unit+" could not be read: "+state.Detail)
		}
	}

	if len(repos) == 0 {
		issues = append(issues, "no repository registered — run `rec-deploy repo add <owner/repo>`")
	} else {
		healthy = append(healthy, plural(len(repos), "repository")+" registered")
	}
	failed := 0
	for _, path := range paths {
		if path.Status == store.StatusFailed || path.Status == store.StatusInterrupted || path.Status == store.StatusRolledBack {
			failed++
		}
	}
	if failed > 0 {
		issues = append(issues, plural(failed, "installation")+" needs attention")
	} else if len(paths) > 0 {
		healthy = append(healthy, plural(len(paths), "installation")+" without a recorded failure")
	}

	if len(issues) > 0 {
		ui.Out("")
		ui.Title(fmt.Sprintf("needs attention (%d)", len(issues)))
		for _, issue := range issues {
			ui.Warn(issue)
		}
	}
	if len(healthy) > 0 {
		ui.Out("")
		ui.Title(fmt.Sprintf("healthy (%d)", len(healthy)))
		for _, item := range healthy {
			ui.Success(item)
		}
	}
}

// unitStates compares each unit systemd resolved against the copy this binary
// ships.
//
// self-update replaces only the binary, so the units a box runs are whichever
// ones its original installer left — and every update widens the gap, silently.
// Reporting it is the whole fix: `install.sh` re-run takes the units from the
// same verified tag as the binary, whereas a repair from inside self-update would
// put a root-owned unit rewrite in the one unattended path whose rollback can only
// restore a binary.
func unitStates(ctx context.Context) []units.Status {
	if !systemd.Available() {
		return nil
	}

	out := make([]units.Status, 0, len(units.Names))
	for _, name := range units.Names {
		// The path systemd resolved, never one inferred from a directory: /etc
		// shadows /lib, so a box installed by two routes runs the copy systemd
		// picked and a guess would compare the file nobody runs.
		s := units.Compare(name, systemd.FragmentPath(ctx, name))

		// A masked unit resolves to a /dev/null symlink, which reads as zero bytes
		// with no error — so it would compare as drifted and be answered with "re-run
		// the installer", which the mask would simply beat again. Masking is a
		// deliberate act; report it as itself.
		if systemd.LoadState(ctx, name) == systemd.LoadMasked {
			s.State = units.StateMasked
		}

		out = append(out, s)
	}

	return out
}

// repoStates renders the registered repositories for --json. The webhook secret
// and the URL token are never emitted, only whether they are set.
func repoStates(repos []store.Repo) []map[string]any {
	out := make([]map[string]any, 0, len(repos))
	for _, r := range repos {
		out = append(out, map[string]any{
			"repository": r.Repository,
			"key_id":     r.GitHubKeyID,
			"hook_id":    r.GitHubHookID,
			"token":      tokenState(r.Token),
			"secret":     tokenState(r.Secret),
			"created_at": r.CreatedAt,
		})
	}

	return out
}

// pathStates renders the last deploy of each checkout for --json.
func pathStates(paths []store.DeployPath) []map[string]any {
	out := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		out = append(out, map[string]any{
			"path":        p.Path,
			"user":        p.User,
			"ran_as_root": p.RanAsRoot,
			"status":      p.Status,
			"reason":      p.Reason,
			"sha":         p.NewSHA,
		})
	}

	return out
}

// renderRepoStates lists the registered repositories with their webhook address,
// the token in it masked.
func renderRepoStates(repos []store.Repo) {
	if len(repos) == 0 {
		ui.Warn("no repository is registered — run `rec-deploy repo add <owner/repo>`")

		return
	}

	rows := make([][2]string, 0, len(repos))
	for _, r := range repos {
		rows = append(rows, [2]string{r.Repository, redactedHookURL(r.Token)})
	}

	ui.Title("repositories")
	ui.Out(ui.TwoCol(rows))
}

// renderPathStates lists the last deploy of every checkout. A root-owned target
// is flagged here as it is everywhere else.
func renderPathStates(paths []store.DeployPath) {
	if len(paths) == 0 {
		ui.Info("no deploy has run on this server yet")

		return
	}

	rows := make([][2]string, 0, len(paths))
	for _, p := range paths {
		rows = append(rows, [2]string{p.Path, strings.Join(pathFlags(p), "  ")})
	}

	ui.Title("last deploy per path")
	ui.Out(ui.TwoCol(rows))
}

// pathFlags describes one checkout's last deploy: its outcome, the user it ran
// as, the commit it landed on, and why it skipped or failed.
func pathFlags(p store.DeployPath) []string {
	flags := []string{p.Status}
	if p.User != "" {
		flags = append(flags, p.User)
	}
	if p.RanAsRoot {
		flags = append(flags, "⚠ root")
	}
	if p.NewSHA != "" {
		flags = append(flags, shortSHA(p.NewSHA))
	}
	if p.Reason != "" {
		flags = append(flags, p.Reason)
	}

	return flags
}

// healthURL is the address status probes: the public URL GitHub delivers to when
// one is configured — that is the path a webhook actually travels — otherwise
// the local listen address. A wildcard bind (0.0.0.0, ::, or a bare :9000) is
// not an address to connect to, so it probes the loopback the daemon is
// listening on too.
func healthURL(cfg *config.Config) string {
	if url := strings.TrimSpace(cfg.PublicURL); url != "" {
		return strings.TrimRight(url, "/") + "/health"
	}

	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return "http://" + cfg.Listen + "/health"
	}

	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}

	return "http://" + net.JoinHostPort(host, port) + "/health"
}

// daemonUp reports whether GET url answers 200 within two seconds. The daemon is
// either answering now or it is not: status must not hang on a public URL a
// firewall drops.
func daemonUp(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == http.StatusOK
}

// daemonLifecycle is the state statusMenuOptions offers actions for: whether
// systemd manages the daemon at all, and if it does, whether the daemon is
// running. Splitting this out of statusMenuOptions lets the invariant — never
// offer both start and stop — be tested by passing a state directly, instead
// of only through the real systemd.Available() a dev box without systemd
// always resolves the same way.
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

// lifecycleOptions is the pure half of statusMenuOptions: given the daemon's
// state, decide which service actions apply. A running daemon has nothing to
// start; a stopped one has nothing to stop or restart.
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

// statusMenuOptions are the actions the status screen offers below its report:
// discovery, and the service lifecycle. Only the transition that applies is
// offered — a running daemon has nothing to start.
func statusMenuOptions(ctx context.Context) []ui.Option {
	items := append([]ui.DescribedOption{
		{Name: "scan", Description: "show every checkout discovery finds", Value: "scan"},
	}, lifecycleOptions(currentDaemonLifecycle(ctx))...)

	return append(ui.DescribedOptions(items...), ui.Option{Label: "Back", Value: "back"})
}

// statusMenu runs the action menu under the printed status report. Bubble Tea
// renders inline, so the report stays on screen above it — the same way the
// banner stays above the hub.
func statusMenu(cmd *cobra.Command) error {
	return (ui.Menu{
		Title:      "Actions",
		Options:    func() []ui.Option { return statusMenuOptions(cmd.Context()) },
		Help:       func() string { return commandHelp(cmd) },
		BackValues: map[string]bool{"back": true},
		Handle:     func(choice string) error { return runStatusAction(cmd, choice) },
	}).Run()
}

// runStatusAction dispatches one status action. scan is a top-level command, so
// it is dispatched from the root rather than from status.
func runStatusAction(cmd *cobra.Command, choice string) error {
	if choice == "scan" {
		return dispatch(cmd.Root(), "scan")
	}

	return serviceAction(cmd.Context(), choice)
}

// serviceAction starts, stops or restarts the webhook daemon. Stopping and
// restarting interrupt whatever the daemon is doing, so they confirm first.
func serviceAction(ctx context.Context, action string) error {
	if action != "start" {
		ok, err := ui.Confirm("Really "+action+" "+daemonUnit+"?", "A deploy running right now is cut short. Its delivery is already spent, so GitHub will not send it again.")
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	var err error
	switch action {
	case "start":
		err = systemd.Start(ctx, daemonUnit)
	case "stop":
		err = systemd.Stop(ctx, daemonUnit)
	case "restart":
		err = systemd.Restart(ctx, daemonUnit)
	default:
		return fmt.Errorf("unknown status action %q", action)
	}
	if err != nil {
		return err
	}

	if systemd.IsActive(ctx, daemonUnit) {
		ui.Success(daemonUnit + " is active")
	} else {
		ui.Warn(daemonUnit + " is not running — read why with `journalctl -u " + daemonUnit + " -n 50`")
	}

	return nil
}

// newLogsCmd builds `logs [owner/repo]`: the deploy history, and with --path the
// command-by-command output of one checkout's last deploy.
func newLogsCmd() *cobra.Command {
	var (
		path  string
		limit int
	)

	cmd := &cobra.Command{
		Use:     "logs [owner/repo]",
		Short:   "Show the deploy history",
		Long:    "logs prints the deploys this server ran, newest first. With --path it prints the exit code, duration and captured output of every command of that checkout's last deploy — the diagnostic a failed deploy is read with.",
		Args:    cobra.MaximumNArgs(1),
		Example: "rec-deploy logs\nrec-deploy logs rdcstarr/tema-mea --limit 5\nrec-deploy logs rdcstarr/tema-mea --path /var/www/api",
		RunE: func(cmd *cobra.Command, args []string) error {
			var slug string
			if len(args) > 0 {
				slug = args[0]
			}

			if path == "" {
				if isInteractive() && !flagJSON {
					return logsBrowser(cmd.Context(), slug, limit)
				}

				return listLogs(cmd.Context(), slug, limit)
			}

			// Deploys are recorded under the absolute path discovery reports, so
			// --path ./site and a trailing slash have to resolve to that string.
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}

			return pathLog(cmd.Context(), slug, abs)
		},
	}

	cmd.Flags().StringVar(&path, "path", "", "show the last deploy of this checkout, command by command")
	cmd.Flags().IntVar(&limit, "limit", 20, "how many deploys to show")

	return cmd
}

// listLogs prints the deploy history, newest first, optionally narrowed to one
// repository.
func listLogs(ctx context.Context, slug string, limit int) error {
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if slug != "" {
		if _, err := registeredRepo(ctx, st, slug); err != nil {
			return err
		}
	}

	deploys, err := st.Deploys(ctx, slug, limit)
	if err != nil {
		return err
	}

	names, err := repoNames(ctx, st)
	if err != nil {
		return err
	}

	if flagJSON {
		out := make([]map[string]any, 0, len(deploys))
		for _, d := range deploys {
			out = append(out, deployJSON(d, names[d.RepoID]))
		}

		return ui.PrintJSON(out)
	}

	if len(deploys) == 0 {
		if slug != "" {
			ui.Warn(slug + " has never been deployed from this server — deploy it with `rec-deploy deploy " + slug + "`")

			return nil
		}

		ui.Warn("no deploy has run on this server yet — deploy one with `rec-deploy deploy <owner/repo>`")

		return nil
	}

	rows := make([][2]string, 0, len(deploys))
	for _, d := range deploys {
		rows = append(rows, deployRow(d, names[d.RepoID]))
	}

	ui.Title(plural(len(deploys), "deploy"))
	ui.Out(ui.TwoCol(rows))
	ui.Info("`rec-deploy logs <owner/repo> --path <path>` shows what each command of a checkout's last deploy printed")

	return nil
}

// deployRow renders one deploy of the history: when it ran, against what it did.
func deployRow(d store.Deploy, repository string) [2]string {
	flags := []string{d.Status}
	if repository != "" {
		flags = append(flags, repository)
	}
	if d.SHA != "" {
		flags = append(flags, shortSHA(d.SHA))
	}
	if branch := strings.TrimPrefix(d.Ref, "refs/heads/"); branch != "" {
		flags = append(flags, branch)
	}
	if subject := subject(d.Message); subject != "" {
		flags = append(flags, subject)
	}
	if d.Author != "" {
		flags = append(flags, d.Author)
	}

	return [2]string{d.StartedAt.Format(time.DateTime), strings.Join(flags, "  ")}
}

// deployJSON renders one deploy for --json. A running deploy has no finish time,
// and an unfinished timestamp is left out rather than printed as a zero date.
func deployJSON(d store.Deploy, repository string) map[string]any {
	out := map[string]any{
		"id":         d.ID,
		"repository": repository,
		"status":     d.Status,
		"ref":        d.Ref,
		"sha":        d.SHA,
		"message":    d.Message,
		"author":     d.Author,
		"started_at": d.StartedAt,
	}
	if !d.FinishedAt.IsZero() {
		out["finished_at"] = d.FinishedAt
	}

	return out
}

// pathLog prints the last deploy of one checkout, command by command: the exit
// code, the duration and the captured output of every step. The summary says a
// deploy failed; this says which command failed and what it printed.
func pathLog(ctx context.Context, slug, path string) error {
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if slug != "" {
		if _, err := registeredRepo(ctx, st, slug); err != nil {
			return err
		}
	}

	deploys, err := st.Deploys(ctx, slug, pathLogSearch)
	if err != nil {
		return err
	}

	names, err := repoNames(ctx, st)
	if err != nil {
		return err
	}

	// Deploys come back newest first, so the first one that touched the path is
	// its last deploy.
	for _, d := range deploys {
		paths, err := st.DeployPaths(ctx, d.ID)
		if err != nil {
			return err
		}

		if p, ok := findPath(paths, path); ok {
			return renderPathLog(d, p, names[d.RepoID])
		}
	}

	return fmt.Errorf("no deploy of %s is recorded — `rec-deploy logs` lists the deploys this server ran", path)
}

// logsBrowser is the interactive deploy history: a repository, then its
// deploys, then the checkouts of one deploy, then what each command of one
// checkout printed. It replaces a printed list that told the operator which
// flags to type next.
//
// The two ways to reach this browser are not symmetric. `rec-deploy logs
// <owner/repo>` names its repository on the command line: there is no picker
// above the deploy list, so Esc on it is the top of this screen and ui.ErrBack
// must keep propagating, the same way it does from every other command
// launched directly. `rec-deploy logs` with no argument opens pickLogsRepo
// first: that picker is now a navigation level of its own, so Esc on the
// deploy list has somewhere to land that isn't the rec-deploy hub. Looping
// over "pick a repository, then browse it" — instead of picking once above a
// single Run — is what gives it that landing spot; collapsing the loop back
// into a single call would silently reintroduce the skip.
func logsBrowser(ctx context.Context, slug string, limit int) error {
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if slug != "" {
		if _, err := registeredRepo(ctx, st, slug); err != nil {
			return err
		}

		return logsDeployMenu(ctx, st, slug, limit)
	}

	for {
		picked, err := pickLogsRepo(ctx, st)
		if err != nil {
			return err // ui.ErrBack (climb to the hub) or ui.ErrQuit (quit)
		}

		if err := logsDeployMenu(ctx, st, picked, limit); !errors.Is(err, ui.ErrBack) {
			return err
		}
	}
}

// logsDeployMenu lists one repository's deploys and opens the one the
// operator picks. ui.ErrBack from Esc on the list itself is read differently
// by logsBrowser's two entry paths — see its doc comment.
func logsDeployMenu(ctx context.Context, st *store.Store, slug string, limit int) error {
	return (ui.Menu{
		Title:      ui.ScreenPath("rec-deploy", "Logs", slug),
		SelectHelp: "open deploy",
		Options:    func() []ui.Option { return logsDeployOptions(ctx, st, slug, limit) },
		Handle:     func(id string) error { return openDeployLog(ctx, st, id) },
	}).Run()
}

// pickLogsRepo chooses which repository's history to browse, annotating each
// row with when that repository last deployed and how it went — the fact the
// operator opened logs to find. It is deliberately not pickRepo: annotating
// there would change the first screen of deploy, rollback and four repo
// commands too.
func pickLogsRepo(ctx context.Context, st *store.Store) (string, error) {
	repos, err := st.Repos(ctx)
	if err != nil {
		return "", err
	}
	if len(repos) == 0 {
		return "", fmt.Errorf("no repository is registered — run `rec-deploy repo add <owner/repo>`")
	}

	items := make([]ui.DescribedOption, 0, len(repos))
	for _, r := range repos {
		items = append(items, ui.DescribedOption{Name: r.Repository, Description: lastDeploySummary(ctx, st, r.Repository), Value: r.Repository})
	}

	choice, err := ui.Select(ui.ScreenPath("rec-deploy", "Logs"), ui.DescribedOptions(items...))
	if err != nil {
		return "", err // ui.ErrBack (re-show the hub) or ui.ErrQuit (quit)
	}
	if choice == "" {
		return "", ui.ErrBack
	}

	return choice, nil
}

// lastDeploySummary describes a repository's most recent deploy in one phrase.
func lastDeploySummary(ctx context.Context, st *store.Store, repository string) string {
	deploys, err := st.Deploys(ctx, repository, 1)
	if err != nil || len(deploys) == 0 {
		return "never deployed"
	}

	return deploys[0].StartedAt.Format(time.DateTime) + " · " + deploys[0].Status
}

// logsDeployOptions lists one repository's deploys, newest first, for the logs
// browser. The repository is left out of each row: the screen is already
// scoped to it.
func logsDeployOptions(ctx context.Context, st *store.Store, slug string, limit int) []ui.Option {
	deploys, err := st.Deploys(ctx, slug, limit)
	if err != nil {
		ui.RenderError(err)

		return nil
	}

	items := make([]ui.DescribedOption, 0, len(deploys))
	for _, d := range deploys {
		row := deployRow(d, "")
		items = append(items, ui.DescribedOption{Name: row[0], Description: row[1], Value: strconv.FormatInt(d.ID, 10)})
	}

	return ui.DescribedOptions(items...)
}

// openDeployLog opens one deploy: straight to the output when it touched a
// single checkout, through a checkout picker when it touched several.
func openDeployLog(ctx context.Context, st *store.Store, id string) error {
	deployID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return err
	}

	d, err := st.DeployByID(ctx, deployID)
	if err != nil {
		return err
	}
	paths, err := st.DeployPaths(ctx, deployID)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		ui.Warn("this deploy recorded no checkout — discovery found none, or it failed before it ran")

		return nil
	}

	names, err := repoNames(ctx, st)
	if err != nil {
		return err
	}
	repository := names[d.RepoID]

	if len(paths) == 1 {
		return showPathLog(d, paths[0], repository)
	}

	return (ui.Menu{
		Title:      ui.ScreenPath("rec-deploy", "Logs", repository, d.StartedAt.Format(time.DateTime)),
		SelectHelp: "open output",
		Options: func() []ui.Option {
			items := make([]ui.DescribedOption, 0, len(paths))
			for _, p := range paths {
				items = append(items, ui.DescribedOption{Name: p.Path, Description: strings.Join(append([]string{p.Status}, userFlags(p)...), "  "), Value: p.Path})
			}

			return ui.DescribedOptions(items...)
		},
		Handle: func(path string) error {
			p, ok := findPath(paths, path)
			if !ok {
				return nil
			}

			return showPathLog(d, p, repository)
		},
	}).Run()
}

// showPathLog opens one checkout's output in a scrollable pane. ui.ErrBack from
// the pane is what returns to the list above it.
func showPathLog(d store.Deploy, p store.DeployPath, repository string) error {
	return (ui.Document{
		Title: ui.ScreenPath("rec-deploy", "Logs", p.Path),
		Body:  pathLogBody(d, p, repository),
	}).Run()
}

// pathLogSearch is how far back `logs --path` looks for the last deploy that
// touched the path. A checkout is deployed by every push to its repository, so
// its last run is within the recent history — but a path skipped for being on
// another branch is still recorded, and the search must reach past those.
const pathLogSearch = 100

// renderPathLog prints one checkout's result of one deploy, command by command.
func renderPathLog(d store.Deploy, p store.DeployPath, repository string) error {
	if flagJSON {
		return ui.PrintJSON(map[string]any{
			"repository":   repository,
			"path":         p.Path,
			"user":         p.User,
			"ran_as_root":  p.RanAsRoot,
			"status":       p.Status,
			"reason":       p.Reason,
			"previous_sha": p.PreviousSHA,
			"new_sha":      p.NewSHA,
			"started_at":   d.StartedAt,
			"commands":     parseCommands(p.Commands),
		})
	}

	ui.Out(pathLogBody(d, p, repository))

	return nil
}

// pathLogBody renders one checkout's result of one deploy: the header, then
// every command with its exit code, duration and captured output. It builds a
// string rather than printing so the same rendering serves `logs --path` and
// the interactive browser's scrollable pane.
//
// The header is built from ui.KeyValueLine, not ui.TwoCol: `logs --path`'s
// non-TTY output is a hard compatibility surface, and TwoCol's two-space
// indent, dropped colon and dynamically sized column are a different format,
// not a restyling of the same one.
func pathLogBody(d store.Deploy, p store.DeployPath, repository string) string {
	var b strings.Builder

	b.WriteString(ui.Heading(p.Path) + "\n")
	b.WriteString(ui.KeyValueLine("repository", repository) + "\n")
	b.WriteString(ui.KeyValueLine("when", d.StartedAt.Format(time.DateTime)) + "\n")
	b.WriteString(ui.KeyValueLine("status", p.Status) + "\n")
	b.WriteString(ui.KeyValueLine("user", strings.Join(userFlags(p), "  ")) + "\n")
	if p.NewSHA != "" {
		b.WriteString(ui.KeyValueLine("commit", shortSHA(p.PreviousSHA)+" → "+shortSHA(p.NewSHA)) + "\n")
	}
	if p.Reason != "" {
		b.WriteString(ui.KeyValueLine("reason", p.Reason) + "\n")
	}

	cmds := parseCommands(p.Commands)
	if len(cmds) == 0 {
		b.WriteString("\n" + ui.Dim("this deploy ran no command on this path") + "\n")

		return b.String()
	}

	for _, c := range cmds {
		b.WriteString("\n" + commandBlock(c))
	}

	return b.String()
}

// userFlags renders the identity the commands ran as, flagging a root-owned
// target: push access to that repository is root on this server.
func userFlags(p store.DeployPath) []string {
	flags := []string{p.User}
	if p.RanAsRoot {
		flags = append(flags, "⚠ root")
	}

	return flags
}

// commandBlock renders one pipeline step: the command, how it ended, and the
// output tail the engine captured.
func commandBlock(c deploy.CommandResult) string {
	head := "$ " + c.Command + "  " + ui.Dim(fmt.Sprintf("exit %d in %s", c.ExitCode, c.Duration.Round(time.Millisecond)))
	if c.TimedOut {
		head += "  " + ui.Dim("(timed out)")
	}

	var b strings.Builder
	if c.ExitCode == 0 && !c.TimedOut {
		b.WriteString(ui.Good("✓") + " " + head + "\n")
	} else {
		b.WriteString(ui.Alert("!") + " " + head + "\n")
	}

	for _, line := range strings.Split(strings.TrimRight(c.Output, "\n"), "\n") {
		if line != "" {
			b.WriteString("    " + ui.Dim(line) + "\n")
		}
	}

	return b.String()
}

// parseCommands reads the per-command results out of the JSON column the engine
// wrote them to. A column an older or crashed run left unreadable yields no
// commands rather than an error — the deploy's status and reason are still worth
// showing — but it is logged: an empty output pane and a pane that could not be
// read look identical, and only one of them is a bug.
func parseCommands(s string) []deploy.CommandResult {
	var cmds []deploy.CommandResult
	if err := json.Unmarshal([]byte(s), &cmds); err != nil {
		slog.Warn("cannot read the recorded command results", "error", err)

		return nil
	}

	return cmds
}

// findPath returns the result recorded for path within one deploy.
func findPath(paths []store.DeployPath, path string) (store.DeployPath, bool) {
	for _, p := range paths {
		if p.Path == path {
			return p, true
		}
	}

	return store.DeployPath{}, false
}

// repoNames maps repository row IDs to slugs: a deploy row carries the ID, and
// the history is read by name.
func repoNames(ctx context.Context, st *store.Store) (map[int64]string, error) {
	repos, err := st.Repos(ctx)
	if err != nil {
		return nil, err
	}

	names := make(map[int64]string, len(repos))
	for _, r := range repos {
		names[r.ID] = r.Repository
	}

	return names, nil
}

// subject trims a commit message to its first line, capped, so one deploy stays
// one row.
func subject(message string) string {
	line, _, _ := strings.Cut(message, "\n")

	if r := []rune(line); len(r) > 60 {
		return string(r[:59]) + "…"
	}

	return line
}
