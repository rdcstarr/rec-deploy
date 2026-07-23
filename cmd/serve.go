package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/cli"
	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/deploy"
	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/mcpserver"
	"github.com/rdcstarr/rec-deploy/internal/notify"
	"github.com/rdcstarr/rec-deploy/internal/server"
	"github.com/rdcstarr/rec-deploy/internal/sshkey"
	"github.com/rdcstarr/rec-deploy/internal/store"
	"github.com/rdcstarr/rec-deploy/internal/systemd"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newServeCmd builds `serve`: the long-running webhook daemon systemd runs.
func newServeCmd() *cobra.Command {
	var listen string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the webhook daemon",
		Long: "serve receives GitHub push webhooks and deploys every checkout of the pushed repository on this server. " +
			"It is the process systemd runs (rec-deploy.service) rather than something to start by hand: " +
			"start, stop and restart it from `rec-deploy status`, and read what it is doing with `journalctl -u rec-deploy -f`.",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{annotationInteractive: "false"},
		Example:     "rec-deploy serve\nrec-deploy serve --listen 127.0.0.1:9000",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// A daemon's journal must show its deploys; -v already set Debug in
			// PersistentPreRunE, so only raise the default (Warn, for every other
			// command) to Info here.
			if !flagVerbose {
				cli.SetupLogger(slog.LevelInfo)
			}

			cfg := Config()
			if listen != "" {
				cfg.Listen = listen
			}

			if err := serveGuard(ctx, cfg.Listen); err != nil {
				return err
			}

			st, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			// A deploy the last process never finished is stranded `running`, and
			// nothing else will ever correct it: its delivery is spent, so a
			// redelivery is a no-op 200. Settle those before serving, or the row
			// lies in `logs` and `status` for good.
			if n, err := st.ReconcileInterrupted(ctx); err != nil {
				return err
			} else if n > 0 {
				slog.Warn("marked deploys interrupted — a previous process was killed mid-deploy", "count", n)
			}

			// Pinned host keys replace StrictHostKeyChecking=no. Do it once, at
			// startup, so a deploy never races the fetch — and so an unreachable
			// GitHub is reported now rather than during the first push.
			if err := pinHostKeys(ctx); err != nil {
				return err
			}

			keysDir, err := config.KeysDir()
			if err != nil {
				return err
			}
			locksDir, err := config.LocksDir()
			if err != nil {
				return err
			}
			knownHosts, err := config.KnownHostsFile()
			if err != nil {
				return err
			}

			ui.Info("listening on " + cfg.Listen)

			// The daemon must see config edits made while it runs — the operator who
			// configures a channel and pushes a minute later gets notified from the
			// startup snapshot otherwise. Reload per accepted delivery; a broken edit
			// falls back to the startup config rather than silencing notifications.
			reload := func() *config.Config {
				return reloadConfig(flagConfig, cfg)
			}

			opts := server.Options{
				Config: cfg,
				Store:  st,
				Deploy: func(ctx context.Context, repo store.Repo, deployID int64, ev github.PushEvent) {
					fcfg := reload()
					deployAndRecord(ctx, st, fcfg, deployID, deploy.Options{
						Repository: repo.Repository,
						Ref:        ev.Ref,
						SHA:        ev.SHA,
						Message:    ev.Message,
						Author:     ev.Author,
						Roots:      fcfg.Discovery.Roots,
						Prune:      fcfg.Discovery.Prune,
						KeysDir:    keysDir,
						LocksDir:   locksDir,
						KnownHosts: knownHosts,
					})
				},
			}
			if !cfg.MCP.Enabled || cfg.MCP.Mode == "cloudflare" {
				return server.ListenAndServe(ctx, opts)
			}
			if cfg.MCP.TokenHash == "" {
				return fmt.Errorf("remote MCP is enabled without a token — run `rec-deploy mcp token rotate`")
			}

			mcpHTTP := &http.Server{
				Addr:              cfg.MCP.Listen,
				Handler:           mcpserver.New(cfg, st).HTTPHandler(cfg.MCP.TokenHash),
				ReadHeaderTimeout: 10 * time.Second,
			}
			serveCtx, cancelServe := context.WithCancel(ctx)
			defer cancelServe()
			mcpErrors := make(chan error, 1)
			go func() { mcpErrors <- mcpHTTP.ListenAndServe() }()
			slog.Info("remote MCP listening", "addr", cfg.MCP.Listen)

			webhookErrors := make(chan error, 1)
			go func() { webhookErrors <- server.ListenAndServe(serveCtx, opts) }()
			select {
			case err := <-mcpErrors:
				cancelServe()
				<-webhookErrors
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return fmt.Errorf("MCP listen on %s: %w", cfg.MCP.Listen, err)
			case err := <-webhookErrors:
				shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				_ = mcpHTTP.Shutdown(shutdown)
				return err
			case <-ctx.Done():
				shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				_ = mcpHTTP.Shutdown(shutdown)
				return <-webhookErrors
			}
		},
	}

	cmd.Flags().StringVar(&listen, "listen", "", "address to bind (default from config)")

	return cmd
}

// underSystemd reports whether this process IS a systemd service rather than
// something started beside one. systemd sets INVOCATION_ID for every process it
// spawns for a unit, and for nothing else.
//
// Without this check the guard below shot the daemon it was meant to protect:
// Type=simple marks a unit active the moment ExecStart forks, so the daemon
// asked "is rec-deploy.service active?", answered yes about itself, and exited 1
// — into Restart=on-failure, a five-second loop, and a server whose webhooks
// silently never deployed anything.
func underSystemd() bool {
	_, ok := os.LookupEnv("INVOCATION_ID")

	return ok
}

// serveGuard refuses to start a second daemon beside the one systemd is already
// running. It has to run before anything else in serve: ReconcileInterrupted
// settles deploys that a killed process left `running`, and against a live
// daemon that means stamping its in-flight deploys `interrupted`. Their
// deliveries are already recorded, so a redelivery is a no-op 200 and nothing
// would ever deploy them again.
//
// The port probe races anything that binds between the check and the real
// listen; that is fine. Its job is to fail before the state is touched, not to
// be the only thing that reports a busy port.
func serveGuard(ctx context.Context, listen string) error {
	if !underSystemd() && systemd.Available() && systemd.IsActive(ctx, daemonUnit) {
		return fmt.Errorf("%s is already running this daemon — follow it with `journalctl -u %s -f`, or stop it first with `systemctl stop %s`", daemonUnit, daemonUnit, daemonUnit)
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("%s is already in use — read the daemon's log with `journalctl -u %s -f`, or serve elsewhere with `rec-deploy serve --listen host:port`: %w", listen, daemonUnit, err)
	}

	return ln.Close()
}

// reloadConfig re-reads the config file at file and returns the fresh value,
// or fallback with an slog.Error when the reload fails — a deleted file or a
// malformed edit must not silence notifications for every delivery after it.
// It is a named function, not inlined into serve's closure, so the per-delivery
// reload seam is testable without an HTTP round trip.
func reloadConfig(file string, fallback *config.Config) *config.Config {
	fresh, err := config.Load(file)
	if err != nil {
		slog.Error("config reload failed — using the startup config", "error", err)
		return fallback
	}

	return fresh
}

// openStore opens the SQLite state database at its configured location.
func openStore(ctx context.Context) (*store.Store, error) {
	path, err := config.StateDB()
	if err != nil {
		return nil, err
	}

	return store.Open(ctx, path)
}

// pinHostKeys refreshes github.com's pinned SSH host keys. A fetch failure is
// survivable only when a previously pinned file is already on disk; with
// neither, git would have no way to verify github.com, and running with an
// unverified host key is exactly the defect this replaces.
func pinHostKeys(ctx context.Context) error {
	path, err := config.KnownHostsFile()
	if err != nil {
		return err
	}

	// The site user's ssh reads this file, so its directory must be traversable
	// by that user. The keys (0700) and the database (0600) inside it stay
	// unreadable: 0711 grants lookup, not listing.
	if err := os.MkdirAll(filepath.Dir(path), 0o711); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o711); err != nil {
		return fmt.Errorf("set state dir mode: %w", err)
	}

	fetchErr := sshkey.WriteKnownHosts(ctx, path)
	if fetchErr == nil {
		return nil
	}

	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("cannot pin github.com host keys and none are cached in %s: %w", path, fetchErr)
	}

	slog.Warn("cannot refresh github.com host keys, using the cached ones", "path", path, "error", fetchErr)

	return nil
}

// recordTimeout bounds the recording of a finished deploy. It is short: the work
// is a handful of local SQLite writes and one notification, and it runs while
// the daemon is shutting down, where nothing may hang.
const recordTimeout = 30 * time.Second

// deployAndRecord is the daemon's deploy callback: it runs one deploy and
// records the outcome. It is a named function, not the closure it wires, because
// the context below is the whole guarantee of the drain and has to be testable
// on its own.
//
// server.Drain ends a deploy that outstays the shutdown budget by cancelling
// ctx. Recording and notifying must still happen — that is the entire point of
// draining — so they run on a context that survives that cancellation. Handing
// them ctx would cancel the very database writes that move the row out of
// `running`, and the drain would produce the zombie row it exists to prevent.
func deployAndRecord(ctx context.Context, st *store.Store, cfg *config.Config, deployID int64, opts deploy.Options) {
	res, err := deploy.Run(ctx, opts)
	if err != nil {
		slog.Error("deploy failed", "repository", opts.Repository, "error", err)
	}

	rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()

	recordResult(rec, st, cfg, deployID, res, err)
}

// recordResult persists every path of a finished deploy, stamps the deploy's
// terminal status, and notifies. It never returns an error: a database or
// channel failure must not swallow the report. A failed deploy and a deploy
// that found zero installations both notify — the old implementation's silence about
// them is the defect this exists to fix.
func recordResult(ctx context.Context, st *store.Store, cfg *config.Config, deployID int64, res deploy.Result, runErr error) {
	for _, pr := range res.Paths {
		if err := st.DeployPathInsert(ctx, store.DeployPath{
			DeployID:    deployID,
			Path:        pr.Path,
			User:        pr.User,
			RanAsRoot:   pr.RanAsRoot,
			PreviousSHA: pr.PreviousSHA,
			NewSHA:      pr.NewSHA,
			Status:      pr.Status,
			Reason:      pr.Reason,
			Commands:    commandsJSON(pr.Commands),
		}); err != nil {
			slog.Error("cannot record deploy path", "path", pr.Path, "error", err)
		}
	}

	if err := st.DeployFinish(ctx, deployID, res.Status); err != nil {
		slog.Error("cannot finish deploy", "deploy", deployID, "error", err)
	}

	sum := notify.Summary{
		Repository: res.Repository,
		Ref:        res.Ref,
		SHA:        res.SHA,
		Message:    res.Message,
		Author:     res.Author,
		Status:     res.Status,
	}
	if runErr != nil {
		sum.Error = runErr.Error()
	}
	for _, pr := range res.Paths {
		sum.Paths = append(sum.Paths, notify.PathSummary{
			Path:      pr.Path,
			User:      pr.User,
			Status:    pr.Status,
			Reason:    pr.Reason,
			RanAsRoot: pr.RanAsRoot,
		})
	}

	notify.Send(ctx, cfg.Notify, sum)
}

// commandsJSON renders a path's command results for the deploy_paths.commands
// column, which is JSON and never NULL.
func commandsJSON(cmds []deploy.CommandResult) string {
	if len(cmds) == 0 {
		return "[]"
	}

	b, err := json.Marshal(cmds)
	if err != nil {
		slog.Error("cannot marshal command results", "error", err)

		return "[]"
	}

	return string(b)
}
