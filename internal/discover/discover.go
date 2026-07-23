// Package discover walks the configured roots looking for checkouts that carry
// a supported rec-deploy YAML manifest, so a freshly cloned target is picked up
// on the next push with no registration step.
//
// It never runs git: root invoking git inside a user-owned tree trips git's
// "dubious ownership" refusal, so the branch and the origin remote are read
// straight out of .git/HEAD and .git/config.
package discover

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/rdcstarr/rec-deploy/internal/manifest"
)

// Installation is one checkout of a repository found on this server.
type Installation struct {
	// Path is the checkout's root directory.
	Path string
	// Repository is the owner/repo slug, read from the checkout's origin remote
	// and falling back to the manifest's declaration when origin is unreadable.
	// Either source alone would leave a checkout with one broken input
	// unattributable, and an unattributed checkout is dropped from its own push
	// rather than reported. It is empty only when both are unreadable.
	Repository string
	// Branch is the branch currently checked out.
	Branch string
	// User is the UNIX owner of Path — the identity every command runs as.
	User string
	// UID and GID are that owner's numeric IDs.
	UID, GID int
	// RanAsRoot marks a root-owned target. Allowed, but flagged loudly: push
	// access to such a repository is root on this server.
	RanAsRoot bool
	// RemoteHTTPS marks a checkout whose origin is an HTTPS URL, which cannot
	// use the deploy key.
	RemoteHTTPS bool
	// Inconsistent names the first file whose owner differs from Path's, if any
	// — a stray root-owned file under a user-owned tree surfaces later as a
	// cryptic `git clean -fd` permission error, so it is reported here instead.
	Inconsistent string
	// Manifest is the parsed rec-deploy YAML manifest; nil when Err is set.
	Manifest *manifest.Manifest
	// Err records why this installation is unusable (bad manifest, unreadable
	// git metadata). It is reported, never silently skipped.
	Err error
}

// Options configures the walk.
type Options struct {
	// Roots are glob patterns the walk starts from.
	Roots []string
	// Prune are directory names never descended into.
	Prune []string
}

// Scan walks the roots and returns every checkout carrying a supported manifest,
// including the ones whose manifest is broken (with Err set).
func Scan(ctx context.Context, opts Options) ([]Installation, error) {
	prune := make(map[string]bool, len(opts.Prune))
	for _, p := range opts.Prune {
		prune[p] = true
	}

	seen := map[string]bool{}
	var out []Installation

	for _, root := range opts.Roots {
		matches, err := filepath.Glob(root)
		if err != nil {
			return nil, fmt.Errorf("bad discovery root %q: %w", root, err)
		}

		for _, base := range matches {
			err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					return nil // unreadable directory — skip it, do not abort the scan
				}
				if !d.IsDir() {
					return nil
				}
				if path != base && prune[d.Name()] {
					return fs.SkipDir
				}
				if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
					return nil
				}

				// A repository root. Record it if it declares a manifest, then
				// stop: whatever is nested below belongs to this checkout.
				if manifest.Exists(path) && !seen[path] {
					seen[path] = true
					out = append(out, inspect(path, opts.Prune))
				}

				return fs.SkipDir
			})
			if err != nil {
				return nil, err
			}
		}
	}

	return out, nil
}

// Filter narrows a scan to one repository. The match is case-insensitive, like
// inspect's own origin-vs-manifest check: GitHub treats slugs that way, so an
// exact comparison here would drop a checkout discovery itself considers healthy.
func Filter(all []Installation, repository string) []Installation {
	var out []Installation
	for _, in := range all {
		if strings.EqualFold(in.Repository, repository) {
			out = append(out, in)
		}
	}

	return out
}

// inspect reads everything rec-deploy needs to know about one checkout. It never
// returns an error: an unusable installation is returned with Err set so the
// caller can report it.
func inspect(path string, prune []string) Installation {
	in := Installation{Path: path}

	fi, err := os.Stat(path)
	if err != nil {
		in.Err = err
		return in
	}

	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		in.Err = fmt.Errorf("%s: cannot read ownership", path)
		return in
	}
	in.UID, in.GID = int(st.Uid), int(st.Gid)
	in.RanAsRoot = in.UID == 0

	// Owner detection is the stat, never a path regex: HestiaCP, /var/www, /srv
	// and /opt all work identically.
	if u, err := user.LookupId(strconv.Itoa(in.UID)); err == nil {
		in.User = u.Username
	} else {
		in.User = strconv.Itoa(in.UID)
	}

	// Identity is resolved from both sources before either failure can return,
	// because an installation left with no Repository is dropped by Filter — it
	// vanishes from the very push it belongs to, deploys nothing, reports
	// nothing, and lets a healthy sibling turn the run green. Attributing it from
	// a single fallible source only moves that silence from one broken input to
	// the other. Origin wins when readable, being the authoritative name; the
	// manifest's declaration is the fallback, enough to carry the checkout to the
	// engine and have it reported as failed. Only with both unreadable is a
	// checkout genuinely unattributable.
	slug, https, originErr := OriginSlug(path)
	m, manifestErr := manifest.Load(path)

	switch {
	case originErr == nil:
		in.Repository, in.RemoteHTTPS = slug, https
	case manifestErr == nil:
		in.Repository = m.Repository
	}

	if originErr != nil {
		in.Err = originErr
		return in
	}
	if manifestErr != nil {
		in.Err = manifestErr
		return in
	}
	in.Manifest = m

	branch, err := Branch(path)
	if err != nil {
		in.Err = err
		return in
	}
	in.Branch = branch

	// The manifest's repository is verified against origin — an old implementation never
	// checks this, so a manifest copied between projects deploys the wrong code.
	if !strings.EqualFold(slug, m.Repository) {
		in.Err = fmt.Errorf("%s: manifest declares %s but origin is %s", path, m.Repository, slug)
		return in
	}

	in.Inconsistent = strayOwner(path, in.UID, prune)

	return in
}

// Branch returns the branch checked out in dir, read from .git/HEAD. A detached
// HEAD has no branch and yields an error: the push filter can never match it.
func Branch(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		return "", fmt.Errorf("read HEAD of %s: %w", dir, err)
	}

	ref, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "ref: refs/heads/")
	if !ok || ref == "" {
		return "", fmt.Errorf("%s: HEAD is detached — check out a branch", dir)
	}

	return ref, nil
}

// OriginSlug returns the owner/repo of dir's origin remote and whether that
// remote is an HTTPS URL. It parses .git/config rather than shelling out to git.
func OriginSlug(dir string) (slug string, https bool, err error) {
	f, err := os.Open(filepath.Join(dir, ".git", "config"))
	if err != nil {
		return "", false, fmt.Errorf("read git config of %s: %w", dir, err)
	}
	defer func() { _ = f.Close() }()

	var url string
	inOrigin := false

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = line == `[remote "origin"]`
			continue
		}
		if inOrigin {
			if v, ok := strings.CutPrefix(line, "url = "); ok {
				url = strings.TrimSpace(v)
				break
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", false, err
	}
	if url == "" {
		return "", false, fmt.Errorf("%s: no origin remote — run `rec-deploy repo install` to clone it", dir)
	}

	return parseSlug(url)
}

// parseSlug pulls owner/repo out of any of git's GitHub URL forms.
func parseSlug(url string) (slug string, https bool, err error) {
	rest := url

	switch {
	case strings.HasPrefix(rest, "git@"):
		// scp-like: git@github.com:owner/repo.git
		_, rest, _ = strings.Cut(rest, ":")
	case strings.HasPrefix(rest, "ssh://"):
		_, rest, _ = strings.Cut(rest, "://")
		_, rest, _ = strings.Cut(rest, "/")
	case strings.HasPrefix(rest, "https://"), strings.HasPrefix(rest, "http://"):
		https = true
		_, rest, _ = strings.Cut(rest, "://")
		_, rest, _ = strings.Cut(rest, "/")
	default:
		return "", false, fmt.Errorf("unrecognized origin url %q", url)
	}

	rest = strings.TrimSuffix(strings.Trim(rest, "/"), ".git")
	if owner, repo, ok := strings.Cut(rest, "/"); !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", false, fmt.Errorf("cannot read owner/repo from origin url %q", url)
	}

	return rest, https, nil
}

// strayOwner walks the checkout and returns the first file whose owner is not
// uid, or "" when the tree is consistent.
func strayOwner(root string, uid int, prune []string) string {
	skip := make(map[string]bool, len(prune))
	for _, p := range prune {
		skip[p] = true
	}

	var stray string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != root && skip[d.Name()] {
			return fs.SkipDir
		}

		fi, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if ok && int(st.Uid) != uid {
			stray = path
			return fs.SkipAll
		}

		return nil
	})

	return stray
}
