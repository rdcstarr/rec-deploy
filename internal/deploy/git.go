package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rdcstarr/rec-deploy/internal/privexec"
)

// headSHA returns the commit currently checked out — the rollback point.
func headSHA(ctx context.Context, o privexec.Options) (string, error) {
	res, err := privexec.Run(ctx, "git rev-parse HEAD", o)
	if err != nil {
		return "", fmt.Errorf("read current sha: %w", err)
	}

	return strings.TrimSpace(res.Output), nil
}

// sync fast-forwards the checkout to origin/<branch>, hard. Running as the
// directory's owner keeps new files correctly owned — php-fpm keeps working —
// and avoids git's "dubious ownership" refusal.
func sync(ctx context.Context, branch string, o privexec.Options) error {
	for _, cmd := range []string{
		"git fetch --all --prune",
		"git reset --hard " + shellQuote("origin/"+branch),
		"git clean -fd",
	} {
		if _, err := privexec.Run(ctx, cmd, o); err != nil {
			return err
		}
	}

	return nil
}

// resetTo puts the tree back on sha — the rollback.
func resetTo(ctx context.Context, sha string, o privexec.Options) error {
	if _, err := privexec.Run(ctx, "git reset --hard "+shellQuote(sha), o); err != nil {
		return err
	}
	_, err := privexec.Run(ctx, "git clean -fd", o)

	return err
}

// useSSHRemote rewrites an HTTPS origin to its SSH form. The deploy key only
// authenticates over SSH, so a checkout cloned with `git clone https://…` would
// ignore it entirely and fail on any private repository. discover has already
// verified that this origin names the repository the manifest declares, so the
// rewrite cannot point the checkout somewhere new.
func useSSHRemote(ctx context.Context, repository string, o privexec.Options) error {
	url := "git@github.com:" + repository + ".git"

	if _, err := privexec.Run(ctx, "git remote set-url origin "+shellQuote(url), o); err != nil {
		return fmt.Errorf("rewrite origin to ssh: %w", err)
	}
	slog.Info("rewrote origin to ssh", "path", o.Dir, "url", url)

	return nil
}

// shellQuote renders s as one single-quoted shell word. Every command privexec
// runs goes through /bin/sh -c, so a branch name or a URL is quoted rather than
// interpolated raw — defense in depth, even though the values reaching here are
// already constrained upstream.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
