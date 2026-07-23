package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/discover"
	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/manifest"
	"github.com/rdcstarr/rec-deploy/internal/privexec"
	"github.com/rdcstarr/rec-deploy/internal/sshkey"
	"github.com/rdcstarr/rec-deploy/internal/store"
	"github.com/rdcstarr/rec-deploy/internal/ui"
)

// newRepoCmd builds the `repo` group: GitHub-side administration of the
// repositories this server deploys.
func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Administer repositories, deploy keys and webhooks",
		Long:  "repo registers a repository on GitHub — a read-only deploy key and this server's webhook — and clones it onto this server.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isInteractive() {
				return listRepos(cmd.Context())
			}

			return repoMenu(cmd)
		},
	}

	cmd.AddCommand(newRepoAddCmd(), newRepoListCmd(), newRepoShowCmd(),
		newRepoInstallCmd(), newRepoRotateCmd(), newRepoRemoveCmd())

	return cmd
}

// repoMenu is the interactive hub for the repo group. The group's top menu
// returns ui.ErrBack, so ← climbs to the rec-deploy hub.
func repoMenu(cmd *cobra.Command) error {
	return (ui.Menu{
		Title: ui.ScreenPath("rec-deploy", "Repositories"),
		Options: func() []ui.Option {
			return []ui.Option{
				{Label: "add     " + ui.Dim("register a repository: deploy key + webhook"), Value: "add"},
				{Label: "list    " + ui.Dim("every registered repository"), Value: "list"},
				{Label: "show    " + ui.Dim("one repository and its installations"), Value: "show"},
				{Label: "install " + ui.Dim("clone a repository into a path"), Value: "install"},
				{Label: "rotate  " + ui.Dim("roll the webhook secret and the deploy key"), Value: "rotate"},
				{Label: "remove  " + ui.Dim("delete the key and the webhook on GitHub"), Value: "remove"},
			}
		},
		Help:   func() string { return commandHelp(cmd) },
		Handle: func(choice string) error { return dispatch(cmd, choice) },
	}).Run()
}

// newRepoAddCmd builds `repo add owner/repo`: generate the deploy key, upload it
// read-only, register this server's webhook, and persist the IDs.
func newRepoAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "add <owner/repo>",
		Short:   "Register a repository: deploy key + webhook",
		Args:    cobra.MaximumNArgs(1),
		Example: "rec-deploy repo add rdcstarr/tema-mea",
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, ok, err := interactiveArg(args, "Repository (owner/repo)")
			if err != nil {
				return err
			}
			if !ok {
				return cmd.Help()
			}

			return repoAdd(cmd.Context(), slug)
		},
	}
}

// repoAdd registers slug on GitHub and on this server. Everything it creates on
// GitHub is undone if any later step fails: a half-registered repository — a key
// or a hook nobody here points at — is worse than none.
func repoAdd(ctx context.Context, slug string) error {
	if err := github.ValidateSlug(slug); err != nil {
		return err
	}

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if _, err := st.RepoByName(ctx, slug); err == nil {
		return fmt.Errorf("%s is already registered — roll its credentials with `rec-deploy repo rotate %s`, or remove it first with `rec-deploy repo remove %s`", slug, slug, slug)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	client, err := githubClient(ctx)
	if err != nil {
		return err
	}

	publicURL, err := resolvePublicURL()
	if err != nil {
		return err
	}

	hookToken, err := github.NewToken()
	if err != nil {
		return err
	}
	secret, err := github.NewSecret()
	if err != nil {
		return err
	}
	hookURL, err := github.HookURL(publicURL, hookToken)
	if err != nil {
		return err
	}

	keysDir, err := config.KeysDir()
	if err != nil {
		return err
	}

	host := hostname()
	key, err := sshkey.Generate("rec-deploy:" + slug + "@" + host)
	if err != nil {
		return err
	}

	var keyID int64
	if err := ui.Spinner("Uploading the deploy key…", func() error {
		keyID, err = client.AddDeployKey(ctx, slug, "rec-deploy ("+host+")", key.Public)

		return err
	}); err != nil {
		return err
	}

	var hookID int64
	if err := ui.Spinner("Registering the webhook…", func() error {
		hookID, err = client.CreateHook(ctx, slug, hookURL, secret)

		return err
	}); err != nil {
		return abortAdd(ctx, client, slug, keyID, 0, err)
	}

	// The private key lands root-only in the keys directory. It is never copied
	// into a site user's home — deploys borrow it from an ephemeral agent — and
	// it is never printed.
	if _, err := sshkey.Save(keysDir, slug, key); err != nil {
		return abortAdd(ctx, client, slug, keyID, hookID, err)
	}

	if _, err := st.RepoInsert(ctx, store.Repo{
		Repository:   slug,
		Token:        hookToken,
		Secret:       secret,
		GitHubKeyID:  keyID,
		GitHubHookID: hookID,
	}); err != nil {
		_ = sshkey.Remove(keysDir, slug)

		return abortAdd(ctx, client, slug, keyID, hookID, err)
	}

	if flagJSON {
		// The hook URL carries the delivery token in its path, so --json reports the
		// public URL and the token's state instead — a captured CI log or artifact
		// never records the live token. rotate's --json does the same.
		return ui.PrintJSON(map[string]any{
			"repository": slug,
			"public_url": publicURL,
			"token":      "set",
			"secret":     "set",
			"key_id":     keyID,
			"hook_id":    hookID,
		})
	}

	ui.Success("registered " + slug)
	// The webhook URL is shown in full exactly once, here: GitHub has it now, and
	// the operator may want to check it in the repository's settings. Everywhere
	// else its token is redacted.
	ui.KeyValue("webhook", hookURL)
	ui.KeyValue("secret", redact(secret))
	ui.KeyValue("key id", strconv.FormatInt(keyID, 10))
	ui.KeyValue("hook id", strconv.FormatInt(hookID, 10))
	ui.KeyValue("deploy key", key.Public)
	ui.Info("clone it with `rec-deploy repo install " + slug + " <path>`, or push to deploy an existing checkout")

	return nil
}

// abortAdd reports cause after deleting whatever the registration already
// created on GitHub, so a failed `repo add` leaves nothing behind.
func abortAdd(ctx context.Context, client *github.Client, slug string, keyID, hookID int64, cause error) error {
	// cause may be the context's own cancellation (Ctrl+C), and the cleanup still
	// has to reach GitHub.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	var errs []error
	if hookID != 0 {
		errs = append(errs, client.DeleteHook(ctx, slug, hookID))
	}
	if keyID != 0 {
		errs = append(errs, client.DeleteDeployKey(ctx, slug, keyID))
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%w — and undoing it on github failed too (%v): delete deploy key %d and webhook %d in the settings of %s by hand", cause, err, keyID, hookID, slug)
	}

	return cause
}

// newRepoListCmd builds `repo list`.
func newRepoListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every registered repository",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listRepos(cmd.Context())
		},
	}
}

// listRepos prints every administered repository with the number of checkouts a
// live scan finds for it. It is also what the bare `rec-deploy repo` prints when it
// is piped and has no terminal to open a hub on.
func listRepos(ctx context.Context) error {
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	repos, err := st.Repos(ctx)
	if err != nil {
		return err
	}

	found, err := scanInstallations(ctx)
	if err != nil {
		return err
	}

	if flagJSON {
		out := make([]map[string]any, 0, len(repos))
		for _, r := range repos {
			out = append(out, map[string]any{
				"repository":    r.Repository,
				"key_id":        r.GitHubKeyID,
				"hook_id":       r.GitHubHookID,
				"token":         tokenState(r.Token),
				"secret":        tokenState(r.Secret),
				"installations": len(discover.Filter(found, r.Repository)),
				"created_at":    r.CreatedAt,
			})
		}

		return ui.PrintJSON(out)
	}

	if len(repos) == 0 {
		ui.Warn("no repository is registered — run `rec-deploy repo add <owner/repo>`")

		return nil
	}

	rows := make([][2]string, 0, len(repos))
	for _, r := range repos {
		n := len(discover.Filter(found, r.Repository))
		rows = append(rows, [2]string{r.Repository, fmt.Sprintf("%s  key %d  hook %d  %s",
			plural(n, "installation"), r.GitHubKeyID, r.GitHubHookID, redactedHookURL(r.Token))})
	}

	ui.Title("registered repositories")
	ui.Out(ui.TwoCol(rows))

	return nil
}

// newRepoShowCmd builds `repo show owner/repo`.
func newRepoShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <owner/repo>",
		Short:   "Show one repository and its installations",
		Args:    cobra.MaximumNArgs(1),
		Example: "rec-deploy repo show rdcstarr/tema-mea",
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, ok, err := pickRepo(cmd.Context(), args, "Repository to show")
			if err != nil {
				return err
			}
			if !ok {
				return cmd.Help()
			}

			return showRepo(cmd.Context(), slug)
		},
	}
}

// showRepo prints one repository in detail, with its checkouts read from a live
// scan rather than from a stale inventory.
func showRepo(ctx context.Context, slug string) error {
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	repo, err := registeredRepo(ctx, st, slug)
	if err != nil {
		return err
	}

	all, err := scanInstallations(ctx)
	if err != nil {
		return err
	}
	found := discover.Filter(all, repo.Repository)

	if flagJSON {
		installs := make([]map[string]any, 0, len(found))
		for _, in := range found {
			installs = append(installs, installJSON(in))
		}

		return ui.PrintJSON(map[string]any{
			"repository":    repo.Repository,
			"key_id":        repo.GitHubKeyID,
			"hook_id":       repo.GitHubHookID,
			"token":         tokenState(repo.Token),
			"secret":        tokenState(repo.Secret),
			"created_at":    repo.CreatedAt,
			"installations": installs,
		})
	}

	ui.Title(repo.Repository)
	ui.KeyValue("webhook", redactedHookURL(repo.Token))
	ui.KeyValue("secret", redact(repo.Secret))
	ui.KeyValue("key id", strconv.FormatInt(repo.GitHubKeyID, 10))
	ui.KeyValue("hook id", strconv.FormatInt(repo.GitHubHookID, 10))
	ui.KeyValue("added", repo.CreatedAt.Format(time.DateTime))
	ui.Out("")
	renderInstallations(found)

	return nil
}

// newRepoRemoveCmd builds `repo remove owner/repo`.
func newRepoRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <owner/repo>",
		Short:   "Delete a repository's deploy key and webhook on GitHub",
		Long:    "remove deletes the deploy key and the webhook on GitHub, then forgets the repository here. The checkouts on disk are left alone.",
		Args:    cobra.MaximumNArgs(1),
		Example: "rec-deploy repo remove rdcstarr/tema-mea --yes",
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, ok, err := pickRepo(cmd.Context(), args, "Repository to remove")
			if err != nil {
				return err
			}
			if !ok {
				return cmd.Help()
			}

			return removeRepo(cmd.Context(), slug)
		},
	}
}

// removeRepo deletes the deploy key and the webhook on GitHub — an old implementation
// removes local files only and leaves both on GitHub forever — then drops the
// local key and the row.
func removeRepo(ctx context.Context, slug string) error {
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	repo, err := registeredRepo(ctx, st, slug)
	if err != nil {
		return err
	}

	if !flagYes {
		if !isInteractive() {
			return fmt.Errorf("removing %s deletes its deploy key and webhook on github — re-run with `--yes`", slug)
		}

		ok, err := ui.Confirm("Delete the deploy key and the webhook of "+slug+" on GitHub?", "")
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	client, err := githubClient(ctx)
	if err != nil {
		return err
	}

	if err := ui.Spinner("Deleting the deploy key and the webhook…", func() error {
		return deleteRepoArtifacts(ctx, st, client, repo)
	}); err != nil {
		return err
	}

	if flagJSON {
		return ui.PrintJSON(map[string]any{"repository": slug, "removed": true})
	}

	ui.Success("removed " + slug + " — its deploy key and webhook are gone from github")
	ui.Info("the checkouts on disk are untouched")

	return nil
}

// deleteRepoArtifacts removes everything a registered repository owns: the
// webhook and the deploy key on GitHub, the local private key, and the store
// row. No UI happens here — `repo remove` and `uninstall` wrap it with their
// own interaction.
func deleteRepoArtifacts(ctx context.Context, st *store.Store, client *github.Client, repo store.Repo) error {
	if err := client.DeleteHook(ctx, repo.Repository, repo.GitHubHookID); err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	if err := client.DeleteDeployKey(ctx, repo.Repository, repo.GitHubKeyID); err != nil {
		return fmt.Errorf("delete deploy key: %w", err)
	}

	keysDir, err := config.KeysDir()
	if err != nil {
		return err
	}
	if err := sshkey.Remove(keysDir, repo.Repository); err != nil {
		return err
	}

	return st.RepoDelete(ctx, repo.ID)
}

// newRepoRotateCmd builds `repo rotate owner/repo`.
func newRepoRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rotate <owner/repo>",
		Short:   "Roll the webhook secret and the deploy key",
		Long:    "rotate uploads a fresh deploy key, re-signs the webhook with a fresh HMAC secret, and deletes the old key on GitHub.",
		Args:    cobra.MaximumNArgs(1),
		Example: "rec-deploy repo rotate rdcstarr/tema-mea --yes",
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, ok, err := pickRepo(cmd.Context(), args, "Repository to rotate")
			if err != nil {
				return err
			}
			if !ok {
				return cmd.Help()
			}

			return rotateRepo(cmd.Context(), slug)
		},
	}
}

// rotateRepo replaces the HMAC secret and the deploy key. The new key is
// uploaded before the old one is deleted, so the repository is never left
// without a usable key.
func rotateRepo(ctx context.Context, slug string) error {
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	repo, err := registeredRepo(ctx, st, slug)
	if err != nil {
		return err
	}

	if !flagYes {
		if !isInteractive() {
			return fmt.Errorf("rotating %s invalidates its current deploy key and webhook secret — re-run with `--yes`", slug)
		}

		ok, err := ui.Confirm("Roll the deploy key and the webhook secret of "+slug+"?", "")
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	client, err := githubClient(ctx)
	if err != nil {
		return err
	}

	publicURL, err := resolvePublicURL()
	if err != nil {
		return err
	}

	// The URL token is not rolled: the webhook keeps its address, so a delivery in
	// flight still lands. What an attacker would need — the HMAC secret — is what
	// changes.
	hookURL, err := github.HookURL(publicURL, repo.Token)
	if err != nil {
		return err
	}

	secret, err := github.NewSecret()
	if err != nil {
		return err
	}

	keysDir, err := config.KeysDir()
	if err != nil {
		return err
	}

	host := hostname()
	key, err := sshkey.Generate("rec-deploy:" + slug + "@" + host)
	if err != nil {
		return err
	}

	var keyID int64
	if err := ui.Spinner("Uploading the new deploy key…", func() error {
		keyID, err = client.AddDeployKey(ctx, slug, "rec-deploy ("+host+")", key.Public)

		return err
	}); err != nil {
		return err
	}

	if err := ui.Spinner("Re-signing the webhook…", func() error {
		return client.UpdateHook(ctx, slug, repo.GitHubHookID, hookURL, secret)
	}); err != nil {
		// Nothing here has moved yet: drop the new key and leave the old pair live.
		return abortAdd(ctx, client, slug, keyID, 0, err)
	}

	// GitHub now signs with the new secret, so persisting it is what keeps the
	// next delivery verifiable — a failure here has to be loud, not swallowed.
	oldKeyID := repo.GitHubKeyID
	repo.Secret, repo.GitHubKeyID = secret, keyID

	if _, err := sshkey.Save(keysDir, slug, key); err != nil {
		return fmt.Errorf("%w — github already signs %s with the new secret: run `rec-deploy repo rotate %s --yes` again", err, slug, slug)
	}
	if err := st.RepoUpdate(ctx, repo); err != nil {
		return fmt.Errorf("%w — github already signs %s with the new secret: run `rec-deploy repo rotate %s --yes` again", err, slug, slug)
	}

	// The old key is dead weight from here on; failing to remove it does not undo
	// the rotation, so it is a warning rather than an error.
	if err := ui.Spinner("Deleting the old deploy key…", func() error {
		return client.DeleteDeployKey(ctx, slug, oldKeyID)
	}); err != nil {
		ui.Warn(fmt.Sprintf("the old deploy key is still on github: %v — delete key %d in the settings of %s", err, oldKeyID, slug))
	}

	if flagJSON {
		return ui.PrintJSON(map[string]any{
			"repository": slug,
			"secret":     "set",
			"key_id":     keyID,
			"hook_id":    repo.GitHubHookID,
		})
	}

	ui.Success("rotated " + slug)
	ui.KeyValue("secret", redact(secret))
	ui.KeyValue("key id", strconv.FormatInt(keyID, 10))
	ui.KeyValue("deploy key", key.Public)

	return nil
}

// newRepoInstallCmd builds `repo install owner/repo PATH`.
func newRepoInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "install <owner/repo> <path>",
		Short:   "Clone a repository into a path, as that path's owner",
		Long:    "install clones the repository over SSH with its deploy key, running git as the owner of the parent directory so every file lands correctly owned.",
		Args:    cobra.MaximumNArgs(2),
		Example: "rec-deploy repo install rdcstarr/tema-mea /home/andrei/web/site/public_html/wp-content/themes/tema",
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, ok, err := pickRepo(cmd.Context(), args, "Repository to clone")
			if err != nil {
				return err
			}
			if !ok {
				return cmd.Help()
			}

			var rest []string
			if len(args) > 1 {
				rest = args[1:]
			}

			path, ok, err := interactiveArg(rest, "Path to clone into (absolute)")
			if err != nil {
				return err
			}
			if !ok {
				return cmd.Help()
			}

			return installRepo(cmd.Context(), slug, path)
		},
	}
}

// installRepo clones slug into path as the owner of the destination directory
// when it already exists and is empty, or of its parent otherwise. This supports
// control panels that provision an empty document root ahead of time while
// keeping the checkout owned by the site user from the first file.
func installRepo(ctx context.Context, slug, path string) error {
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	// The deploy key is what the clone authenticates with, so the repository has
	// to be registered before there is anything to clone with.
	repo, err := registeredRepo(ctx, st, slug)
	if err != nil {
		return err
	}

	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}

	ownerDir, err := cloneOwnerDir(path)
	if err != nil {
		if errors.Is(err, errCloneDestinationNotEmpty) {
			ok, confirmErr := confirmClearCloneDestination(path)
			if confirmErr != nil {
				return confirmErr
			}
			if !ok {
				if isInteractive() {
					return ui.ErrBack
				}
				return fmt.Errorf("%s is not empty — re-run with `--yes` to permanently clear it before cloning", path)
			}
			if err := clearDirectoryContents(path); err != nil {
				return err
			}
			ui.Success("cleared " + path)
			ownerDir = path
		} else {
			return err
		}
	}

	// The clone runs as the destination's owner when a control panel created an
	// empty document root, or as the parent's owner for a new destination.
	owner, err := ownerOf(ownerDir)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(owner.Uid)
	gid, _ := strconv.Atoi(owner.Gid)

	if uid != os.Geteuid() && os.Geteuid() != 0 {
		return fmt.Errorf("cloning into %s means running git as %s — re-run as root", path, owner.Username)
	}

	keysDir, err := config.KeysDir()
	if err != nil {
		return err
	}
	key, err := sshkey.Load(keysDir, repo.Repository)
	if err != nil {
		return err
	}

	// Pinned host keys, never StrictHostKeyChecking=no. The fetch reaches the
	// network, so it spins rather than sitting on a dead pause before the clone.
	if err := ui.Spinner("Pinning github.com host keys…", func() error {
		return pinHostKeys(ctx)
	}); err != nil {
		return err
	}
	knownHosts, err := config.KnownHostsFile()
	if err != nil {
		return err
	}

	// The private key is lent to git through an ephemeral agent socket owned by
	// the site user, and dies with this command. It never lands on their disk.
	agent, err := sshkey.StartAgent(key.Private, uid, gid)
	if err != nil {
		return err
	}
	defer func() { _ = agent.Close() }()

	opts := privexec.Options{
		Dir:  filepath.Dir(path),
		User: owner,
		Env: []string{
			"SSH_AUTH_SOCK=" + agent.Socket(),
			"GIT_SSH_COMMAND=" + sshkey.GitSSHCommand(agent.Socket(), knownHosts),
		},
	}
	if !flagJSON {
		opts.Stream = os.Stdout
		ui.Title("cloning " + slug + " into " + path)
	}

	// Always SSH: the deploy key authenticates over nothing else.
	command := "git clone " + shellQuote("git@github.com:"+repo.Repository+".git") + " " + shellQuote(path)

	res, err := privexec.Run(ctx, command, opts)
	if err != nil {
		return err
	}

	hasManifest := manifest.Exists(path)
	if flagJSON && !hasManifest {
		return ui.PrintJSON(map[string]any{
			"repository": repo.Repository,
			"path":       path,
			"user":       owner.Username,
			"output":     res.Output,
		})
	}

	ui.Success("cloned " + slug + " into " + path + " as " + owner.Username)
	if uid == 0 {
		ui.Warn("⚠ root: this checkout is root-owned, so push access to " + slug + " is root on this server")
	}
	if !hasManifest {
		ui.Info("commit a .rec-deploy.yml or .rec-deploy.yaml to the repository, then check `rec-deploy scan` picks the checkout up")
		return nil
	}

	ui.Info("manifest found — running post_deploy")
	return runDeploy(ctx, repo.Repository, path)
}

var errCloneDestinationNotEmpty = errors.New("clone destination is not empty")
var errCloneDestinationSymlink = errors.New("clone destination must not be a symlink")

// cloneOwnerDir validates a clone destination and returns the directory whose
// owner should run git. Git accepts both a missing destination and an existing
// empty directory.
func cloneOwnerDir(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Dir(path), nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect clone destination %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errCloneDestinationSymlink
	}
	if !info.IsDir() {
		return "", errCloneDestinationNotEmpty
	}

	dir, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("inspect clone destination %s: %w", path, err)
	}
	defer func() { _ = dir.Close() }()

	if _, err := dir.Readdirnames(1); err == nil {
		return "", errCloneDestinationNotEmpty
	} else if !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("inspect clone destination %s: %w", path, err)
	}

	return path, nil
}

// confirmClearCloneDestination requires an explicit destructive confirmation.
// The global --yes flag supports automation; non-interactive callers otherwise
// fail closed.
func confirmClearCloneDestination(path string) (bool, error) {
	if flagYes {
		return true, nil
	}
	if !isInteractive() {
		return false, nil
	}

	entries, err := cloneDestinationEntries(path)
	if err != nil {
		return false, err
	}
	desc := "Permanently deletes everything inside this directory, including hidden files"
	if len(entries) > 0 {
		desc += ":\n\n" + strings.Join(entries, "\n")
	}
	return ui.Confirm("Clear "+path+" and continue?", desc)
}

// cloneDestinationEntries returns a compact inventory for the destructive
// confirmation instead of asking the operator to approve an unknown directory.
func cloneDestinationEntries(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("inspect clone destination %s: %w", path, err)
	}

	const shown = 8
	names := make([]string, 0, min(len(entries), shown+1))
	for i, entry := range entries {
		if i == shown {
			names = append(names, fmt.Sprintf("… and %d more", len(entries)-shown))
			break
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, "• "+name)
	}
	return names, nil
}

// clearDirectoryContents removes the entries inside path without replacing the
// directory itself, preserving its owner and control-panel permissions.
func clearDirectoryContents(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("inspect clone destination %s: %w", path, err)
	}
	for _, entry := range entries {
		target := filepath.Join(path, entry.Name())
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("clear clone destination %s: remove %s: %w", path, entry.Name(), err)
		}
	}
	return nil
}

// pickRepo resolves which repository a command acts on: args[0] when given, an
// interactive pick over the registered ones in a terminal, and otherwise no
// value at all — the caller shows its help. It is the shared resolver `repo
// show/remove/rotate/install`, `deploy`, `rollback` and `logs` all use.
func pickRepo(ctx context.Context, args []string, title string) (slug string, ok bool, err error) {
	if len(args) > 0 {
		return args[0], true, nil
	}
	if !isInteractive() {
		return "", false, nil
	}

	st, err := openStore(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = st.Close() }()

	repos, err := st.Repos(ctx)
	if err != nil {
		return "", false, err
	}
	if len(repos) == 0 {
		return "", false, fmt.Errorf("no repository is registered — run `rec-deploy repo add <owner/repo>`")
	}

	options := make([]ui.Option, 0, len(repos))
	for _, r := range repos {
		options = append(options, ui.Option{Label: r.Repository, Value: r.Repository})
	}

	choice, err := ui.Select(title, options)
	if err != nil {
		return "", false, err // ui.ErrBack (re-show the hub) or ui.ErrQuit (quit)
	}
	if choice == "" {
		return "", false, ui.ErrBack
	}

	return choice, true, nil
}

// registeredRepo looks slug up, turning "not found" into the actionable error —
// every repo command needs the row, and none of them can invent it.
func registeredRepo(ctx context.Context, st *store.Store, slug string) (store.Repo, error) {
	if err := github.ValidateSlug(slug); err != nil {
		return store.Repo{}, err
	}

	repo, err := st.RepoByName(ctx, slug)
	if errors.Is(err, store.ErrNotFound) {
		return store.Repo{}, fmt.Errorf("%s is not registered — run `rec-deploy repo add %s`", slug, slug)
	}
	if err != nil {
		return store.Repo{}, err
	}

	return repo, nil
}

// githubClient builds the API client from the resolved token: rec-deploy's config,
// then GITHUB_TOKEN / GH_TOKEN, then the gh CLI.
func githubClient(ctx context.Context) (*github.Client, error) {
	token, err := github.Token(ctx, Config().GitHub.Token)
	if err != nil {
		return nil, err
	}

	return github.New(token), nil
}

// resolvePublicURL returns the URL GitHub delivers to. It is the one thing
// `repo add` cannot invent, so in a terminal it asks for it and saves the
// answer; piped, it points at the wizard.
func resolvePublicURL() (string, error) {
	cfg := Config()
	if url := strings.TrimSpace(cfg.PublicURL); url != "" {
		return url, nil
	}

	notSet := fmt.Errorf("public_url is not configured — run `rec-deploy init`, or `rec-deploy config set public_url http://<ip>:9000`")
	if !isInteractive() {
		return "", notSet
	}

	url, err := ui.Prompt("Public URL GitHub delivers to (e.g. http://1.2.3.4:9000)", "Registered with GitHub as the webhook destination. Must be reachable from the internet — open the port in the firewall. Usually http://<public-ip>:<port>.", "")
	if err != nil {
		return "", err
	}
	if url = strings.TrimSpace(url); url == "" {
		return "", notSet
	}

	cfg.PublicURL = url
	if err := save(); err != nil {
		return "", err
	}

	return url, nil
}

// scanInstallations runs discovery over the configured roots. It blocks and
// prints nothing of its own, so it spins.
func scanInstallations(ctx context.Context) ([]discover.Installation, error) {
	cfg := Config()

	var found []discover.Installation
	err := ui.Spinner("Scanning for installations…", func() error {
		var err error
		found, err = discover.Scan(ctx, discover.Options{Roots: cfg.Discovery.Roots, Prune: cfg.Discovery.Prune})

		return err
	})

	return found, err
}

// renderInstallations prints one line per checkout, flagging everything an
// operator has to see: a root-owned target, an https origin that cannot use the
// deploy key, a tree with stray ownership, and a manifest that will not parse.
func renderInstallations(found []discover.Installation) {
	if len(found) == 0 {
		ui.Warn("no installation on this server — clone one with `rec-deploy repo install <owner/repo> <path>`")

		return
	}

	rows := make([][2]string, 0, len(found))
	for _, in := range found {
		rows = append(rows, [2]string{in.Path, strings.Join(installFlags(in), "  ")})
	}

	ui.Out(ui.TwoCol(rows))
}

// installFlags describes one checkout: its branch, its owner, and every warning
// that applies to it.
func installFlags(in discover.Installation) []string {
	var flags []string
	if in.Branch != "" {
		flags = append(flags, in.Branch)
	}
	if in.User != "" {
		flags = append(flags, in.User)
	}
	if in.RanAsRoot {
		// Push access to this repository is root on this server. Never silent.
		flags = append(flags, "⚠ root")
	}
	if in.RemoteHTTPS {
		flags = append(flags, "⚠ https")
	}
	if in.Inconsistent != "" {
		flags = append(flags, "⚠ mixed ("+in.Inconsistent+")")
	}
	if in.Err != nil {
		flags = append(flags, "✗ "+in.Err.Error())
	}

	return flags
}

// installJSON renders one checkout for --json, without the manifest body and
// with the error as a string.
func installJSON(in discover.Installation) map[string]any {
	out := map[string]any{
		"path":         in.Path,
		"branch":       in.Branch,
		"user":         in.User,
		"ran_as_root":  in.RanAsRoot,
		"remote_https": in.RemoteHTTPS,
	}
	if in.Inconsistent != "" {
		out["inconsistent"] = in.Inconsistent
	}
	if in.Err != nil {
		out["error"] = in.Err.Error()
	}

	return out
}

// redactedHookURL renders the webhook address with its token masked. The token
// alone cannot forge a delivery — the HMAC still has to check out — but an
// unknown token is a 404, and that is a wall worth keeping up.
func redactedHookURL(token string) string {
	url, err := github.HookURL(Config().PublicURL, redact(token))
	if err != nil {
		return "(public_url is not set)"
	}

	return url
}

// hostname is what the deploy key is titled with on GitHub, so a repository
// deployed to several servers shows which key belongs to which.
func hostname() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "unknown-host"
	}

	return host
}

// ownerOf resolves the UNIX owner of dir from the stat, never from the path —
// HestiaCP, /var/www, /srv and /opt all work identically.
func ownerOf(dir string) (*user.User, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("%w — create the parent directory first", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}

	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("%s: cannot read ownership", dir)
	}

	owner, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10))
	if err != nil {
		return nil, fmt.Errorf("unknown owner uid %d of %s: %w", st.Uid, dir, err)
	}

	return owner, nil
}

// shellQuote renders s as one shell word. Every command privexec runs goes
// through /bin/sh -c, so a path is quoted rather than interpolated raw.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// plural renders a count with its noun: "1 installation", "3 installations".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}

	return strconv.Itoa(n) + " " + noun + "s"
}
