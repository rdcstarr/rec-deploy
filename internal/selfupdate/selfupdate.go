// Package selfupdate replaces the running rec-deploy binary with the latest GitHub
// release built for the host OS/arch. It downloads the release archive through
// the GitHub REST API and verifies its SHA-256 against the release's
// checksums.txt before writing anything — the verification fails closed, so an
// archive whose digest is missing or mismatched is never installed. On a server
// rec-deploy usually lives in a root-owned /usr/bin: when the executable's directory
// is not writable it installs with `sudo install` (no token is passed to sudo),
// otherwise it swaps the binary in place (minio/selfupdate).
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	update "github.com/minio/selfupdate"
	"golang.org/x/mod/semver"

	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/retry"
	"github.com/rdcstarr/rec-deploy/internal/systemd"
)

const (
	// repoSlug is the GitHub repository rec-deploy updates itself from.
	repoSlug = "rdcstarr/rec-deploy"

	// binaryName is the executable GoReleaser packs into the release archive.
	binaryName = "rec-deploy"

	// assetPrefix opens every archive name GoReleaser builds for this project:
	// rec-deploy_<version>_<os>_<arch>.tar.gz.
	assetPrefix = binaryName + "_"
)

// Result reports the outcome of a check or update.
type Result struct {
	Current    string `json:"current"`     // running version
	Latest     string `json:"latest"`      // latest released tag
	Newer      bool   `json:"newer"`       // a newer release is available
	Updated    bool   `json:"updated"`     // the binary was replaced
	RolledBack bool   `json:"rolled_back"` // the release would not start and the previous binary was put back

	// Restarted distinguishes "installed and the daemon is now running the new
	// release" from "installed but the daemon was deliberately left stopped" —
	// Updated alone cannot tell those apart, and a caller that only checks Updated
	// would tell the operator the daemon restarted when it is still down. It is
	// true on exactly one path: the daemon was restarted onto the new release and
	// waitHealthy confirmed it stayed up. It is false whenever the restart was
	// skipped (the unit was already stopped) and on every rollback, where the new
	// release never stayed up and the previous binary was restored instead.
	Restarted bool `json:"restarted"`
}

// asset is one file attached to a GitHub release.
type asset struct {
	Name string `json:"name"`
	URL  string `json:"url"` // API asset URL; download with Accept: octet-stream
}

// release is the subset of the GitHub release payload we use.
type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

// Check reports whether a newer release than current exists, changing nothing.
func Check(ctx context.Context, current string) (Result, error) {
	rel, err := latest(ctx, token(ctx))
	if err != nil {
		return Result{}, err
	}

	return Result{Current: current, Latest: rel.TagName, Newer: isNewer(rel.TagName, current)}, nil
}

// Update is a verified release ready to install: the downloaded archive plus
// where and how to write it. Prepare returns it so a caller can show progress
// for the network phase, then Install it (which may stream a sudo prompt)
// without a spinner.
type Update struct {
	Result  Result
	exe     string
	tarball []byte
	sudo    bool
}

// Available reports whether Prepare found a newer release to install.
func (u *Update) Available() bool { return u != nil && u.Result.Newer }

// Prepare checks for a newer release and, when one exists, downloads its archive
// and verifies the archive's SHA-256 against the release's checksums.txt. It
// performs all the network I/O but writes nothing, so a caller may wrap it in a
// progress spinner. The returned Update has Available()=false when already up to
// date.
func Prepare(ctx context.Context, current string) (*Update, error) {
	tok := token(ctx)

	rel, err := latest(ctx, tok)
	if err != nil {
		return nil, err
	}

	res := Result{Current: current, Latest: rel.TagName, Newer: isNewer(rel.TagName, current)}
	if !res.Newer {
		return &Update{Result: res}, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}

	assetName, assetURL, err := hostAsset(rel)
	if err != nil {
		return nil, err
	}

	tarball, err := downloadAsset(ctx, tok, assetURL)
	if err != nil {
		return nil, err
	}
	if err := verifyChecksum(ctx, tok, rel, assetName, tarball); err != nil {
		return nil, err
	}

	return &Update{
		Result:  res,
		exe:     exe,
		tarball: tarball,
		sudo:    os.Geteuid() != 0 && !writable(filepath.Dir(exe)),
	}, nil
}

// Install writes the prepared, checksum-verified update over the running binary.
// When rec-deploy lives in a non-writable, root-owned path it escalates with `sudo
// install`, which streams its password prompt — so callers must NOT wrap Install
// in a spinner. It is a no-op when no newer release was prepared.
func (u *Update) Install(ctx context.Context) (Result, error) {
	if !u.Available() {
		return u.Result, nil
	}

	if u.sudo {
		if err := sudoInstall(ctx, bytes.NewReader(u.tarball), u.exe); err != nil {
			return u.Result, err
		}
	} else if err := applyTarball(bytes.NewReader(u.tarball), u.exe); err != nil {
		return u.Result, fmt.Errorf("replace binary: %w", err)
	}

	res := u.Result
	res.Updated = true

	return res, nil
}

// Apply checks, downloads, verifies, and installs the latest release in one
// call. It is a no-op (Updated=false) when already up to date. Use Prepare +
// Update.Install when you need to show progress for the download phase
// separately from the (possibly sudo-prompting) install.
func Apply(ctx context.Context, current string) (Result, error) {
	u, err := Prepare(ctx, current)
	if err != nil {
		return Result{}, err
	}

	return u.Install(ctx)
}

// token resolves a GitHub token for the release API, tolerating its absence:
// rec-deploy's releases are public, so an unauthenticated request works. A token is
// only worth having for the higher rate limit. It never reads the rec-deploy config
// (that would invert the dependency); the cascade in github.Token falls through
// to GITHUB_TOKEN/GH_TOKEN, then the gh CLI.
func token(ctx context.Context) string {
	tok, err := github.Token(ctx, "")
	if err != nil {
		return ""
	}

	return tok
}

// setAuth adds the Authorization header when a token was resolved. An empty
// token means an anonymous request — valid against a public repository.
func setAuth(req *http.Request, tok string) {
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// applyTarball extracts the rec-deploy binary from a gzip-compressed tar stream and
// atomically replaces the running executable with it.
func applyTarball(r io.Reader, target string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s binary not found in release archive", binaryName)
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == binaryName {
			return update.Apply(tr, update.Options{TargetPath: target})
		}
	}
}

// sudoInstall extracts the rec-deploy binary to a temp file and installs it over dst
// with sudo, for a root-owned location the user cannot write directly. sudo only
// copies an already-downloaded file — no token or environment is forwarded — so
// it works under any sudoers policy (it just prompts for the password).
func sudoInstall(ctx context.Context, tarball io.Reader, dst string) error {
	if _, err := exec.LookPath("sudo"); err != nil {
		return fmt.Errorf("rec-deploy is in a non-writable location (%s) and sudo is unavailable — re-run as root", filepath.Dir(dst))
	}

	tmp, err := extractToTemp(tarball)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()

	cmd := exec.CommandContext(ctx, "sudo", "install", "-m", "0755", tmp, dst)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install %s: %w", dst, err)
	}

	return nil
}

// extractToTemp writes the rec-deploy binary from a gzip-tar stream to a temp file
// and returns its path (mode 0755).
func extractToTemp(tarball io.Reader) (string, error) {
	gz, err := gzip.NewReader(tarball)
	if err != nil {
		return "", fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("%s binary not found in release archive", binaryName)
		}
		if err != nil {
			return "", fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != binaryName {
			continue
		}

		f, err := os.CreateTemp("", "rec-deploy-update-*")
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(f, tr); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return "", err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(f.Name())
			return "", err
		}
		if err := os.Chmod(f.Name(), 0o755); err != nil {
			_ = os.Remove(f.Name())
			return "", err
		}

		return f.Name(), nil
	}
}

// writable reports whether dir can be written to — the executable's directory
// must be, to swap the binary in place without elevation.
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".rec-deploy-update-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)

	return true
}

// latest fetches the repository's latest release via the REST API, retrying
// transient failures (network errors, 429, 5xx) with backoff. A repository with
// no releases answers 404, which is reported as such rather than as a bare HTTP
// status: before the first tag there is simply nothing to update to.
func latest(ctx context.Context, tok string) (release, error) {
	client := &http.Client{Timeout: 20 * time.Second}

	var rel release
	err := retry.Do(ctx, retry.Default, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			github.APIBaseURL+"/repos/"+repoSlug+"/releases/latest", nil)
		if err != nil {
			return retry.Permanent(err)
		}
		setAuth(req, tok)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := client.Do(req)
		if err != nil {
			return err // transient
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return fmt.Errorf("fetch latest release: HTTP %d", resp.StatusCode) // transient
		}
		if resp.StatusCode == http.StatusNotFound {
			return retry.Permanent(fmt.Errorf("no release found for %s", repoSlug))
		}
		if resp.StatusCode != http.StatusOK {
			return retry.Permanent(fmt.Errorf("fetch latest release: HTTP %d", resp.StatusCode))
		}

		var r release
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return retry.Permanent(fmt.Errorf("decode release: %w", err))
		}
		rel = r

		return nil
	})
	if err != nil {
		return release{}, err
	}
	if rel.TagName == "" {
		return release{}, fmt.Errorf("no release found for %s", repoSlug)
	}

	return rel, nil
}

// assetStream GETs a release asset and returns the open response (a gzip
// tarball). It retries transient failures while establishing the download
// (network errors, 429, 5xx); a failure mid-stream is the caller's to handle.
func assetStream(ctx context.Context, tok, url string) (*http.Response, error) {
	client := &http.Client{Timeout: 2 * time.Minute}

	var resp *http.Response
	err := retry.Do(ctx, retry.Default, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return retry.Permanent(err)
		}
		setAuth(req, tok)
		req.Header.Set("Accept", "application/octet-stream")

		r, err := client.Do(req)
		if err != nil {
			return err // transient
		}
		if r.StatusCode == http.StatusTooManyRequests || r.StatusCode >= 500 {
			_ = r.Body.Close()
			return fmt.Errorf("download asset: HTTP %d", r.StatusCode) // transient
		}
		if r.StatusCode != http.StatusOK {
			_ = r.Body.Close()
			return retry.Permanent(fmt.Errorf("download asset: HTTP %d", r.StatusCode))
		}
		resp = r

		return nil
	})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// hostAsset returns the name and API URL of the release archive built for the
// host OS/arch, matching GoReleaser's name_template for this project
// (rec-deploy_<version>_<os>_<arch>.tar.gz).
func hostAsset(rel release) (name, url string, err error) {
	suffix := fmt.Sprintf("_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	for _, a := range rel.Assets {
		if strings.HasPrefix(a.Name, assetPrefix) && strings.HasSuffix(a.Name, suffix) {
			return a.Name, a.URL, nil
		}
	}

	return "", "", fmt.Errorf("release %s has no binary for %s/%s", rel.TagName, runtime.GOOS, runtime.GOARCH)
}

// checksumsURL returns the API URL of the release's checksums.txt asset.
func checksumsURL(rel release) (string, error) {
	for _, a := range rel.Assets {
		if a.Name == "checksums.txt" {
			return a.URL, nil
		}
	}

	return "", fmt.Errorf("release %s has no checksums.txt asset", rel.TagName)
}

// downloadAsset fetches a release asset fully into memory, retrying transient
// failures while establishing the download.
func downloadAsset(ctx context.Context, tok, url string) ([]byte, error) {
	resp, err := assetStream(ctx, tok, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	return io.ReadAll(resp.Body)
}

// verifyChecksum downloads the release's checksums.txt and confirms the SHA-256
// of tarball matches the digest recorded for assetName. It fails closed: a
// missing manifest, a missing entry, or a mismatch all return an error, so the
// binary is never installed unverified.
func verifyChecksum(ctx context.Context, tok string, rel release, assetName string, tarball []byte) error {
	url, err := checksumsURL(rel)
	if err != nil {
		return err
	}

	manifest, err := downloadAsset(ctx, tok, url)
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}

	want, err := digestFor(string(manifest), assetName)
	if err != nil {
		return err
	}

	got := fmt.Sprintf("%x", sha256.Sum256(tarball))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", assetName, want, got)
	}

	return nil
}

// digestFor returns the hex SHA-256 recorded for name in a GoReleaser
// checksums.txt manifest (each line is "<sha256>  <filename>").
func digestFor(manifest, name string) (string, error) {
	for _, line := range strings.Split(manifest, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0], nil
		}
	}

	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

// isNewer reports whether tag is a newer release than current. Both are
// normalized to a leading "v" first — GoReleaser stamps the version without one
// ("0.2.3"), while GitHub release tags carry it ("v0.2.3"). A non-release (dev)
// current version is always treated as outdated.
func isNewer(tag, current string) bool {
	tag, current = withV(tag), withV(current)
	if !semver.IsValid(current) {
		return true
	}

	return semver.Compare(tag, current) > 0
}

// withV gives v the leading "v" that golang.org/x/mod/semver expects.
func withV(v string) string {
	if v != "" && !strings.HasPrefix(v, "v") {
		return "v" + v
	}

	return v
}

const (
	// pollInterval is how long waitHealthy sleeps between is-active samples while
	// watching a freshly restarted unit.
	pollInterval = time.Second

	// minWait floors RestartOptions.Wait. waitHealthy needs three consecutive
	// active samples, one per pollInterval, so a Wait at or below 3×pollInterval
	// would roll back a perfectly healthy release for lack of time. The floor keeps
	// a mis-set (or zero) Wait from doing that.
	minWait = 10 * time.Second
)

// tryRestart and unitActive are the two systemd calls the supervised-restart
// flow makes, held in package-level vars so a test can swap them for fakes and
// drive the rollback branches without a real init system. They default to the
// real implementations and tests restore them with t.Cleanup. This is the
// standard library's own seam idiom (see net's test hooks) — no interface, no
// framework.
var (
	tryRestart = systemd.TryRestart
	unitActive = systemd.IsActive
)

// RestartOptions supervises an unattended update on a systemd host: which unit
// to bring back, where to keep the outgoing binary, and how long to give the
// unit to come back up before the new release is judged bad.
type RestartOptions struct {
	// Unit is the daemon's systemd unit, e.g. "rec-deploy.service".
	Unit string
	// BackupPath is where the outgoing binary is kept for a rollback.
	BackupPath string
	// Wait bounds how long the unit has to come back healthy. It must comfortably
	// exceed 3×pollInterval — the health check needs three consecutive one-second
	// active samples — or a healthy release is rolled back for lack of time; a Wait
	// below minWait is raised to minWait.
	Wait time.Duration
}

// ApplyAndRestart installs the latest release, restarts the daemon, and puts the
// previous binary back when the new one will not stay up. It is the unattended
// path — `rec-deploy self-update --restart`, run by rec-deploy-update.service on
// a timer — so it fails closed harder than anywhere else in this binary: nobody
// is watching, and one bad release would otherwise take down every server.
//
// It must not be called from inside the daemon's own unit: systemd would kill
// the caller's cgroup along with the daemon. That is why the timer runs it from
// a separate oneshot unit.
func ApplyAndRestart(ctx context.Context, current string, opts RestartOptions) (Result, error) {
	u, err := Prepare(ctx, current)
	if err != nil {
		return Result{}, err
	}
	if !u.Available() {
		return u.Result, nil
	}

	if err := backup(u.exe, opts.BackupPath); err != nil {
		return u.Result, err
	}

	res, err := u.Install(ctx)
	if err != nil {
		return res, err
	}

	// Raise a too-small Wait to the floor rather than let waitHealthy roll back a
	// healthy binary that simply had no time to show three active samples. opts is
	// a value, so this touches only the local copy.
	if opts.Wait < minWait {
		opts.Wait = minWait
	}

	return superviseRestart(ctx, res, u.exe, opts, pollInterval)
}

// superviseRestart is ApplyAndRestart's post-install core: decide whether to
// restart the unit at all, watch the new binary stay up, and roll back to the
// kept binary when it does not. It is split out from the network-bound
// Prepare/Install steps so its branches are testable — a test swaps the
// package-level tryRestart and unitActive seams and calls it with a short poll.
func superviseRestart(ctx context.Context, res Result, exe string, opts RestartOptions, poll time.Duration) (Result, error) {
	// The unit is still on the OLD binary here — Install only rewrote the file on
	// disk; systemd has not restarted — so is-active reports whether the operator
	// was running the daemon at all. An inactive unit means it was deliberately
	// stopped: try-restart would no-op on it, and the health poll below would then
	// never see an active unit and roll back on every timer cycle — a recurring
	// false alarm on an update that can never land. Leave a stopped daemon stopped;
	// the new binary waits on disk for whenever the operator starts it next.
	//
	// But that reading is only trustworthy when ctx is still live. unitActive
	// shells out with exec.CommandContext, which returns before systemctl ever
	// runs once ctx is already cancelled or past its deadline — so a dead ctx
	// makes it report false for a genuinely running daemon too, indistinguishable
	// from "operator stopped it". Taking the skip branch on that reading would
	// leave the new, unvetted binary on disk unsupervised. So a dead ctx must NOT
	// take the skip branch; fall through and attempt the restart instead. Its own
	// failure under the same dead ctx is caught below and routed to rollback,
	// which detaches onto a fresh context and restores the old binary — fail
	// closed rather than a false "success".
	if !unitActive(ctx, opts.Unit) && ctx.Err() == nil {
		return res, nil
	}

	if err := tryRestart(ctx, opts.Unit); err != nil {
		return rollback(ctx, res, exe, opts, err)
	}

	active := func(c context.Context) bool { return unitActive(c, opts.Unit) }
	if waitHealthy(ctx, active, opts.Wait, poll) {
		res.Restarted = true

		return res, nil
	}

	return rollback(ctx, res, exe, opts,
		fmt.Errorf("%s did not stay up after updating to %s", opts.Unit, res.Latest))
}

// rollback restores the kept binary and restarts the unit on it. The error it
// returns names the release that failed: the operator has to know which tag to
// stop shipping, and a rollback that reports success is worse than no rollback.
func rollback(ctx context.Context, res Result, exe string, opts RestartOptions, cause error) (Result, error) {
	res.RolledBack = true

	// The recovery detaches from ctx onto a freshly bounded one. waitHealthy
	// returns false the instant ctx is cancelled (operator Ctrl+C, or an outer
	// per-command timeout shorter than opts.Wait), and reusing that dead ctx would
	// make exec.CommandContext return before systemctl ever ran — the old binary
	// back on disk but the daemon never restarted onto it, leaving the bad new code
	// running unsupervised. This is the one recovery path in the binary; it must
	// not inherit the cancellation that sent us here. The restart is a local
	// systemctl call, so a short timeout is ample.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err := restore(exe, opts.BackupPath); err != nil {
		return res, fmt.Errorf("%s is broken and the rollback failed too — restore %s by hand: %w",
			res.Latest, exe, err)
	}

	if err := tryRestart(ctx, opts.Unit); err != nil {
		return res, fmt.Errorf("rolled back to %s but %s would not restart: %w", res.Current, opts.Unit, err)
	}

	return res, fmt.Errorf("rolled back to %s: %w", res.Current, cause)
}

// waitHealthy reports whether the unit is genuinely back up. It requires three
// consecutive active samples rather than one: systemd marks a Type=simple
// service active the moment it forks, so a binary that panics at startup reads
// active for an instant, and Restart=on-failure then flaps it between active and
// failed. A single sample would call a crash-loop healthy.
//
// The boundary this draws is liveness, not readiness: is-active means the process
// forked and has not exited — not that it bound its port or finished initializing.
// The streak catches a release that dies within the first few seconds, the common
// bad-release failure. It deliberately does NOT catch one that clears the streak
// and then dies later (e.g. a panic after a slow pinHostKeys network failure): such
// a release is judged healthy here and left running. Catching it would need a
// readiness probe, which is out of scope.
//
// active is a parameter so the failure path is testable without systemd.
func waitHealthy(ctx context.Context, active func(context.Context) bool, budget, every time.Duration) bool {
	const streak = 3

	deadline := time.Now().Add(budget)

	var seen int
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(every):
		}

		if !active(ctx) {
			seen = 0

			continue
		}

		seen++
		if seen == streak {
			return true
		}
	}

	return false
}

// backup copies the running executable aside so a release that will not start
// can be put back. minio/selfupdate renames the old binary out of the way and
// deletes it once the new one is in place, so its copy is gone exactly when a
// rollback would need it.
func backup(exe, dst string) error {
	if dst == "" {
		return errors.New("no path to keep the outgoing binary — a rollback would be impossible")
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("create the backup directory: %w", err)
	}

	if err := copyFile(exe, dst); err != nil {
		return err
	}

	// copyFile leaves dst 0755, which restore needs (it writes an
	// executable-by-all /usr/bin/rec-deploy). The kept copy needs no such reach:
	// tighten it to the spec's 0500 (r-x, owner only) to honor its least-privilege
	// invariant. root can still read it for a later restore, and the parent dir is
	// already 0700 root-only, so this is defense in depth.
	if err := os.Chmod(dst, 0o500); err != nil {
		return fmt.Errorf("restrict the kept binary to 0500: %w", err)
	}

	return nil
}

// restore puts the kept binary back over the running executable. The write is a
// rename, which replaces the directory entry: overwriting an ELF file that is
// currently executing fails with ETXTBSY.
func restore(exe, src string) error {
	return copyFile(src, exe)
}

// copyFile writes src to dst through a temp file in dst's directory and renames
// it into place, so dst is never seen half-written and an executing dst is
// replaced rather than truncated.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".rec-deploy-copy-*")
	if err != nil {
		return fmt.Errorf("create a temp file next to %s: %w", dst, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("copy %s: %w", src, err)
	}
	// fsync before the rename so a power loss mid-rollback cannot leave dst's
	// directory entry pointing at a partially written inode — this is the one path
	// whose whole job is recovery, and an executable it half-wrote is worse than
	// the broken release it replaces.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("flush the temp copy of %s: %w", src, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close the temp copy of %s: %w", src, err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return fmt.Errorf("make the copy of %s executable: %w", src, err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("move the copy into %s: %w", dst, err)
	}

	return nil
}
