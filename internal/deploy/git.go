package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rdcstarr/rec-deploy/internal/privexec"
)

// runGit executes one git plumbing command under opts.Progress.
//
// The plumbing never streams: the spinner owns the terminal while it runs, and
// git's chatter ("HEAD is now at …", a bare SHA from rev-parse) is machinery,
// not a result. That makes the captured tail the only trace a failure leaves,
// so a failing step records itself on pr and carries an excerpt of its output in
// the error — otherwise silencing the stream would trade noise for a deploy that
// fails with an exit code and no reason. Only the failing step is recorded: a
// successful deploy's Commands stays the pipeline alone, which is what keeps
// `rec-deploy logs` readable and the stored row small.
//
// pr may be nil for a step that runs before there is a PathResult to record on.
func runGit(ctx context.Context, opts Options, pr *PathResult, title, command string, o privexec.Options) (privexec.Result, error) {
	// Dropping Stream here, rather than at the call sites, makes "git plumbing is
	// silent" a property of the plumbing that a new caller cannot forget.
	// privexec.Options is passed by value, so this touches only our copy.
	o.Stream = nil

	var res privexec.Result
	err := opts.step(title, func() error {
		var runErr error
		res, runErr = privexec.Run(ctx, command, o)

		return runErr
	})
	if err == nil {
		return res, nil
	}

	if pr != nil {
		pr.Commands = append(pr.Commands, commandResult(res))
	}
	if excerpt := res.Excerpt(); excerpt != "" {
		err = fmt.Errorf("%w: %s", err, excerpt)
	}

	return res, err
}

// headSHA returns the commit currently checked out — the rollback point.
func headSHA(ctx context.Context, opts Options, pr *PathResult, o privexec.Options) (string, error) {
	res, err := runGit(ctx, opts, pr, "Reading the current commit…", "git rev-parse HEAD", o)
	if err != nil {
		return "", fmt.Errorf("read current sha: %w", err)
	}

	return strings.TrimSpace(res.Output), nil
}

// sync fast-forwards the checkout to origin/<branch>, hard. Running as the
// directory's owner keeps new files correctly owned — php-fpm keeps working —
// and avoids git's "dubious ownership" refusal.
func sync(ctx context.Context, opts Options, pr *PathResult, branch string, o privexec.Options) error {
	for _, s := range []struct{ title, command string }{
		{"Fetching origin…", "git fetch --all --prune"},
		{"Resetting to origin/" + branch + "…", "git reset --hard " + shellQuote("origin/"+branch)},
		{"Cleaning the working tree…", "git clean -fd"},
	} {
		if _, err := runGit(ctx, opts, pr, s.title, s.command, o); err != nil {
			return err
		}
	}

	return nil
}

// resetTo puts the tree back on sha — the rollback.
func resetTo(ctx context.Context, opts Options, pr *PathResult, sha string, o privexec.Options) error {
	if _, err := runGit(ctx, opts, pr, "Restoring the previous commit…", "git reset --hard "+shellQuote(sha), o); err != nil {
		return err
	}
	_, err := runGit(ctx, opts, pr, "Cleaning the working tree…", "git clean -fd", o)

	return err
}

// useSSHRemote rewrites an HTTPS origin to its SSH form. The deploy key only
// authenticates over SSH, so a checkout cloned with `git clone https://…` would
// ignore it entirely and fail on any private repository. discover has already
// verified that this origin names the repository the manifest declares, so the
// rewrite cannot point the checkout somewhere new.
func useSSHRemote(ctx context.Context, opts Options, repository string, o privexec.Options) error {
	url := "git@github.com:" + repository + ".git"

	// prepare has no PathResult yet, so this step's failure travels in the error
	// alone — which still carries git's own message.
	if _, err := runGit(ctx, opts, nil, "Pointing origin at ssh…", "git remote set-url origin "+shellQuote(url), o); err != nil {
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
