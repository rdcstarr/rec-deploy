package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}

	dir := t.TempDir()
	if !writable(dir) {
		t.Errorf("writable(%q) = false, want true for a fresh temp dir", dir)
	}

	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	if writable(ro) {
		t.Errorf("writable(%q) = true, want false for a read-only dir", ro)
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		tag, current string
		want         bool
	}{
		{"v0.2.3", "0.2.3", false},        // GoReleaser current (no "v") vs tag ("v") — equal
		{"v0.2.3", "v0.2.3", false},       // both carry "v"
		{"v0.2.4", "0.2.3", true},         // a newer release
		{"v0.2.3", "0.2.4", false},        // local build is ahead of the release
		{"v0.2.3", "dev", true},           // dev build is always outdated
		{"v0.2.3", "ce45bae-dirty", true}, // pseudo / dirty build
	}
	for _, c := range cases {
		if got := isNewer(c.tag, c.current); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.tag, c.current, got, c.want)
		}
	}
}

// hostAssetName is the archive GoReleaser produces for the host, per this
// project's name_template: rec-deploy_<version>_<os>_<arch>.tar.gz.
func hostAssetName(version string) string {
	return fmt.Sprintf("rec-deploy_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}

func TestHostAsset(t *testing.T) {
	want := hostAssetName("0.3.0")
	rel := release{TagName: "v0.3.0"}
	rel.Assets = append(rel.Assets,
		asset{Name: "checksums.txt", URL: "https://api/checksums"},
		asset{Name: "rec-deploy_0.3.0_plan9_sparc.tar.gz", URL: "https://api/other"},
		asset{Name: want, URL: "https://api/host"},
	)

	name, url, err := hostAsset(rel)
	if err != nil {
		t.Fatalf("hostAsset: %v", err)
	}
	if name != want {
		t.Errorf("name = %q, want %q", name, want)
	}
	if url != "https://api/host" {
		t.Errorf("url = %q, want the host asset URL", url)
	}

	// A release with no archive for this host must fail, never fall back to a
	// foreign binary.
	foreign := release{TagName: "v0.3.0"}
	foreign.Assets = append(foreign.Assets, asset{Name: "rec-deploy_0.3.0_plan9_sparc.tar.gz", URL: "https://api/other"})
	if _, _, err := hostAsset(foreign); err == nil {
		t.Error("hostAsset on a release without a host archive = nil error, want an error")
	}
}

func TestDigestFor(t *testing.T) {
	manifest := "aaaa  rec-deploy_0.3.0_linux_amd64.tar.gz\nbbbb  rec-deploy_0.3.0_darwin_arm64.tar.gz\n"

	got, err := digestFor(manifest, "rec-deploy_0.3.0_darwin_arm64.tar.gz")
	if err != nil {
		t.Fatalf("digestFor: %v", err)
	}
	if got != "bbbb" {
		t.Errorf("digestFor = %q, want %q", got, "bbbb")
	}

	if _, err := digestFor(manifest, "rec-deploy_0.3.0_linux_arm64.tar.gz"); err == nil {
		t.Error("digestFor for an absent asset = nil error, want an error")
	}
}

// checksumServer serves a checksums.txt body and reports how many times it was
// hit, so a test can point a release's asset URL at it.
func checksumServer(t *testing.T, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv
}

// relWithChecksums builds a release whose checksums.txt asset is served by url.
func relWithChecksums(url string) release {
	rel := release{TagName: "v0.3.0"}
	rel.Assets = append(rel.Assets, asset{Name: "checksums.txt", URL: url})

	return rel
}

// TestVerifyChecksumFailsClosed is the security-critical case: the binary is
// installed only when the archive's SHA-256 matches the digest the release
// recorded for it. A mismatch, a missing entry, or a missing checksums.txt is an
// error — never a warning that installs anyway.
func TestVerifyChecksumFailsClosed(t *testing.T) {
	ctx := context.Background()
	tarball := []byte("pretend this is a gzip tarball")
	name := hostAssetName("0.3.0")
	sum := fmt.Sprintf("%x", sha256.Sum256(tarball))

	t.Run("match", func(t *testing.T) {
		srv := checksumServer(t, sum+"  "+name+"\n")
		if err := verifyChecksum(ctx, "", relWithChecksums(srv.URL), name, tarball); err != nil {
			t.Errorf("verifyChecksum on a matching digest = %v, want nil", err)
		}
	})

	t.Run("uppercase digest still matches", func(t *testing.T) {
		srv := checksumServer(t, strings.ToUpper(sum)+"  "+name+"\n")
		if err := verifyChecksum(ctx, "", relWithChecksums(srv.URL), name, tarball); err != nil {
			t.Errorf("verifyChecksum on an uppercase digest = %v, want nil", err)
		}
	})

	t.Run("tampered archive", func(t *testing.T) {
		srv := checksumServer(t, sum+"  "+name+"\n")
		if err := verifyChecksum(ctx, "", relWithChecksums(srv.URL), name, []byte("tampered")); err == nil {
			t.Error("verifyChecksum on a tampered archive = nil, want an error")
		}
	})

	t.Run("no entry for the asset", func(t *testing.T) {
		srv := checksumServer(t, sum+"  some_other_file.tar.gz\n")
		if err := verifyChecksum(ctx, "", relWithChecksums(srv.URL), name, tarball); err == nil {
			t.Error("verifyChecksum with no entry for the asset = nil, want an error")
		}
	})

	t.Run("release has no checksums.txt", func(t *testing.T) {
		rel := release{TagName: "v0.3.0"}
		rel.Assets = append(rel.Assets, asset{Name: name, URL: "https://api/host"})
		if err := verifyChecksum(ctx, "", rel, name, tarball); err == nil {
			t.Error("verifyChecksum without a checksums.txt asset = nil, want an error")
		}
	})
}

// tarGz builds a gzip-compressed tar stream holding one regular file.
func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestExtractToTemp(t *testing.T) {
	// GoReleaser puts the binary at the archive root as "rec-deploy".
	archive := tarGz(t, binaryName, []byte("#!/bin/echo rec-deploy\n"))

	path, err := extractToTemp(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("extractToTemp: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "#!/bin/echo rec-deploy\n" {
		t.Errorf("extracted content = %q, want the binary's bytes", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("extracted mode = %v, want 0755", info.Mode().Perm())
	}
}

func TestExtractToTempRejectsForeignArchive(t *testing.T) {
	// An archive that does not carry a "rec-deploy" binary (e.g. rec's) must fail
	// rather than install whatever executable it happens to hold.
	archive := tarGz(t, "rec", []byte("not rec-deploy"))

	if _, err := extractToTemp(bytes.NewReader(archive)); err == nil {
		t.Error("extractToTemp on an archive without the rec-deploy binary = nil, want an error")
	}
}

// TestApplyTarballUsesStableTarget guards the update race where resolving the
// running executable a second time can observe minio's temporary .old name.
func TestApplyTarballUsesStableTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "rec-deploy")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := tarGz(t, binaryName, []byte("new binary"))
	if err := applyTarball(bytes.NewReader(archive), target); err != nil {
		t.Fatalf("applyTarball: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Errorf("target contains %q, want the new binary", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(target), ".rec-deploy.old")); !os.IsNotExist(err) {
		t.Errorf("temporary old binary remains after update: %v", err)
	}
}

// TestBackupAndRestoreRoundTrip: minio/selfupdate renames the outgoing binary
// aside and then deletes it on success, so its copy cannot be relied on for a
// rollback. We keep our own, and it must come back byte-for-byte.
func TestBackupAndRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rec-deploy")
	kept := filepath.Join(dir, "state", "rec-deploy.prev")

	want := []byte("the outgoing binary\n")
	if err := os.WriteFile(exe, want, 0o755); err != nil {
		t.Fatalf("write exe: %v", err)
	}

	if err := backup(exe, kept); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// The kept copy is least-privilege per the spec: 0500 (r-x, owner only), not
	// the 0755 copyFile writes for restore's executable-by-all /usr/bin target.
	ki, err := os.Stat(kept)
	if err != nil {
		t.Fatalf("stat kept: %v", err)
	}
	if ki.Mode().Perm() != 0o500 {
		t.Errorf("kept binary mode %v, want 0500 — rec-deploy.prev must stay least-privilege", ki.Mode().Perm())
	}

	// The update lands.
	if err := os.WriteFile(exe, []byte("the broken release\n"), 0o755); err != nil {
		t.Fatalf("overwrite exe: %v", err)
	}

	if err := restore(exe, kept); err != nil {
		t.Fatalf("restore: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read exe: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("restored %q, want %q", got, want)
	}

	fi, err := os.Stat(exe)
	if err != nil {
		t.Fatalf("stat exe: %v", err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("restored mode %v, want 0755 — a binary that is not executable is not a rollback", fi.Mode().Perm())
	}
}

// TestBackupRefusesAnEmptyPath: without somewhere to keep the outgoing binary
// there is no rollback, and an unattended update with no rollback is how one bad
// release takes down a fleet. It must fail before anything is replaced. The exe
// is written first so a missing empty-dst guard cannot hide behind os.Open
// failing on a non-existent source — only the guard can produce the error.
func TestBackupRefusesAnEmptyPath(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "rec-deploy")
	if err := os.WriteFile(exe, []byte("the outgoing binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := backup(exe, ""); err == nil {
		t.Fatal("expected an error with no backup path")
	}
}

// TestWaitHealthyRejectsACrashLoop: systemd marks a Type=simple service active
// the moment it forks, so a binary that panics at startup reads active for an
// instant. Restart=on-failure then oscillates it. One sample would call that
// healthy; a streak must not.
func TestWaitHealthyRejectsACrashLoop(t *testing.T) {
	var n int
	flapping := func(context.Context) bool {
		n++

		return n%2 == 0 // active, failed, active, failed…
	}

	if waitHealthy(context.Background(), flapping, 200*time.Millisecond, time.Millisecond) {
		t.Error("a unit flapping between active and failed must not be called healthy")
	}
}

// TestWaitHealthyAcceptsAUnitThatStaysUp is the other half of the contract.
func TestWaitHealthyAcceptsAUnitThatStaysUp(t *testing.T) {
	up := func(context.Context) bool { return true }

	if !waitHealthy(context.Background(), up, 2*time.Second, time.Millisecond) {
		t.Error("a unit that stays active must be called healthy")
	}
}

// swapSeams replaces the systemd seams with fakes for one test and restores the
// real implementations afterward. Package-level function vars swapped under
// t.Cleanup are the vanilla Go seam — the same trick the standard library's net
// package uses for its test hooks — so the rollback branches are exercisable
// without a real init system.
func swapSeams(t *testing.T, restart func(context.Context, string) error, active func(context.Context, string) bool) {
	t.Helper()

	origRestart, origActive := tryRestart, unitActive
	t.Cleanup(func() { tryRestart, unitActive = origRestart, origActive })

	tryRestart, unitActive = restart, active
}

// TestSuperviseRestartLeavesAStoppedDaemonStopped (finding #2): an operator who
// deliberately stopped the daemon must not have it force-restarted and rolled
// back on every timer cycle. When the unit is inactive before the update, the
// new binary is installed but the restart, the health check, and the rollback
// are all skipped — a clean success.
func TestSuperviseRestartLeavesAStoppedDaemonStopped(t *testing.T) {
	var restarted bool
	swapSeams(t,
		func(context.Context, string) error { restarted = true; return nil },
		func(context.Context, string) bool { return false }, // operator stopped it
	)

	opts := RestartOptions{
		Unit:       "rec-deploy.service",
		BackupPath: filepath.Join(t.TempDir(), "rec-deploy.prev"),
		Wait:       50 * time.Millisecond,
	}
	res := Result{Current: "v1.0.0", Latest: "v1.1.0", Updated: true}

	got, err := superviseRestart(context.Background(), res, filepath.Join(t.TempDir(), "rec-deploy"), opts, time.Millisecond)
	if err != nil {
		t.Fatalf("superviseRestart on a deliberately-stopped daemon = %v, want nil", err)
	}
	if restarted {
		t.Error("restarted a unit the operator had stopped")
	}
	if !got.Updated {
		t.Error("Updated = false, want true — the new binary is on disk for the next manual start")
	}
	if got.RolledBack {
		t.Error("RolledBack = true, want false — nothing was rolled back")
	}
	if got.Restarted {
		t.Error("Restarted = true, want false — the daemon was never restarted, only the binary on disk changed")
	}
}

// TestSuperviseRestartSucceedsOnAHealthyRestart is the full-success path: the
// unit was active before the update, the restart succeeds, and it stays active
// long enough to clear waitHealthy's streak. Result.Restarted is the only signal
// a caller has to tell this apart from the deliberately-stopped skip path above
// — both return Updated=true, RolledBack=false, and a nil error.
func TestSuperviseRestartSucceedsOnAHealthyRestart(t *testing.T) {
	var restarted bool
	swapSeams(t,
		func(context.Context, string) error { restarted = true; return nil },
		func(context.Context, string) bool { return true }, // active before and after the restart
	)

	opts := RestartOptions{
		Unit:       "rec-deploy.service",
		BackupPath: filepath.Join(t.TempDir(), "rec-deploy.prev"),
		Wait:       50 * time.Millisecond,
	}
	res := Result{Current: "v1.0.0", Latest: "v1.1.0", Updated: true}

	got, err := superviseRestart(context.Background(), res, filepath.Join(t.TempDir(), "rec-deploy"), opts, time.Millisecond)
	if err != nil {
		t.Fatalf("superviseRestart on a healthy restart = %v, want nil", err)
	}
	if !restarted {
		t.Error("tryRestart was not called for a unit the operator was running")
	}
	if !got.Updated {
		t.Error("Updated = false, want true")
	}
	if got.RolledBack {
		t.Error("RolledBack = true, want false — the release stayed up")
	}
	if !got.Restarted {
		t.Error("Restarted = false, want true — the daemon was restarted and waitHealthy confirmed it stayed up")
	}
}

// TestSuperviseRestartRollsBackAnUnhealthyRelease: the unit was active before the
// update but never reaches the required active streak afterward, so the new
// binary is judged bad, the kept binary is restored, and the returned error
// names the release that failed.
func TestSuperviseRestartRollsBackAnUnhealthyRelease(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rec-deploy")
	kept := filepath.Join(dir, "rec-deploy.prev")
	if err := os.WriteFile(exe, []byte("the broken release\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kept, []byte("the previous binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var restarts int
	var calls int
	swapSeams(t,
		func(context.Context, string) error { restarts++; return nil },
		func(context.Context, string) bool { calls++; return calls == 1 }, // active for the pre-check, dead ever after
	)

	opts := RestartOptions{Unit: "rec-deploy.service", BackupPath: kept, Wait: 30 * time.Millisecond}
	res := Result{Current: "v1.0.0", Latest: "v1.1.0", Updated: true}

	got, err := superviseRestart(context.Background(), res, exe, opts, time.Millisecond)
	if err == nil {
		t.Fatal("superviseRestart on an unhealthy release = nil error, want a rollback error")
	}
	if !strings.Contains(err.Error(), res.Latest) {
		t.Errorf("error %q does not name the failed release %q", err, res.Latest)
	}
	if !got.RolledBack {
		t.Error("RolledBack = false, want true")
	}
	if got.Restarted {
		t.Error("Restarted = true, want false — the new release never stayed up, so the old binary was restored instead")
	}
	if restarts != 2 {
		t.Errorf("tryRestart called %d times, want 2 (restart onto new, then recovery onto old)", restarts)
	}

	back, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != "the previous binary\n" {
		t.Errorf("exe after rollback = %q, want the previous binary restored", back)
	}
}

// TestSuperviseRestartRecoversOnACancelledContext (finding #1): when the health
// wait is cut short by a cancelled context, the rollback's recovery restart must
// still run. Reusing the dead context would make exec.CommandContext return
// before systemctl ran, leaving the old binary on disk but the bad new code
// running unsupervised. The recovery therefore restarts on a context detached
// from the cancelled one.
func TestSuperviseRestartRecoversOnACancelledContext(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rec-deploy")
	kept := filepath.Join(dir, "rec-deploy.prev")
	if err := os.WriteFile(exe, []byte("the broken release\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kept, []byte("the previous binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var restartCtxCancelled []bool
	var calls int
	swapSeams(t,
		func(c context.Context, _ string) error {
			restartCtxCancelled = append(restartCtxCancelled, c.Err() != nil)

			return nil
		},
		func(context.Context, string) bool { calls++; return calls == 1 }, // active pre-restart, then the wait is cut short
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: operator Ctrl+C, or an outer timeout shorter than Wait

	opts := RestartOptions{Unit: "rec-deploy.service", BackupPath: kept, Wait: time.Minute}
	res := Result{Current: "v1.0.0", Latest: "v1.1.0", Updated: true}

	got, err := superviseRestart(ctx, res, exe, opts, time.Millisecond)
	if err == nil {
		t.Fatal("superviseRestart with a cancelled context = nil error, want a rollback error")
	}
	if !got.RolledBack {
		t.Error("RolledBack = false, want true")
	}
	if got.Restarted {
		t.Error("Restarted = true, want false — the recovery restored the old binary, it did not restart onto the new one")
	}
	if len(restartCtxCancelled) != 2 {
		t.Fatalf("tryRestart called %d times, want 2 (initial restart, then recovery)", len(restartCtxCancelled))
	}
	// The initial restart saw the cancelled context; the recovery restart must not
	// — on the dead context systemctl would be a no-op and the daemon would never
	// come back up.
	if !restartCtxCancelled[0] {
		t.Error("initial restart context was not cancelled — the test no longer exercises the cancelled path")
	}
	if restartCtxCancelled[1] {
		t.Error("recovery restart ran on a cancelled context — the daemon would never come back up")
	}
}

// TestSuperviseRestartDoesNotSkipOnADeadContext: the "operator stopped it" skip
// branch samples unitActive right after Install, before the unit has been
// touched. If ctx is already cancelled or past its deadline at that exact
// sample — an outer per-command timeout, or Ctrl+C, expiring in the window
// after Install returns — unitActive cannot run systemctl at all and reports
// false regardless of the daemon's true state (exec.CommandContext returns
// before the command ever runs on a dead ctx). Misreading that as "operator
// stopped it" would return a bare success and leave the new, unvetted binary on
// disk unsupervised. A dead ctx must instead fall through to the
// restart-and-supervise path, where tryRestart's own dead-context failure is
// caught and routed to rollback.
func TestSuperviseRestartDoesNotSkipOnADeadContext(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rec-deploy")
	kept := filepath.Join(dir, "rec-deploy.prev")
	if err := os.WriteFile(exe, []byte("the broken release\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kept, []byte("the previous binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var restarts int
	swapSeams(t,
		// Models the real TryRestart: exec.CommandContext fails to even start the
		// process once ctx is already done, so the call errors on a dead ctx and
		// succeeds on a live one.
		func(c context.Context, _ string) error {
			restarts++
			if c.Err() != nil {
				return c.Err()
			}

			return nil
		},
		// Models the real IsActive under a dead ctx: exec.CommandContext never runs
		// systemctl, so query returns "" and IsActive reports false — regardless of
		// whether the daemon is actually running.
		func(context.Context, string) bool { return false },
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before superviseRestart ever samples unitActive

	opts := RestartOptions{Unit: "rec-deploy.service", BackupPath: kept, Wait: 30 * time.Millisecond}
	res := Result{Current: "v1.0.0", Latest: "v1.1.0", Updated: true}

	got, err := superviseRestart(ctx, res, exe, opts, time.Millisecond)
	if err == nil {
		t.Fatal(`superviseRestart with a dead ctx and an inactive-looking unit = nil error, want a rollback error — a dead ctx must not be read as "operator stopped it"`)
	}
	if !got.RolledBack {
		t.Error("RolledBack = false, want true — the unvetted binary must not be left running unsupervised")
	}
	if got.Restarted {
		t.Error("Restarted = true, want false — the rollback restored the old binary, it did not restart onto the new one")
	}
	if restarts != 2 {
		t.Errorf("tryRestart called %d times, want 2 (the skipped-branch bug never calls it at all)", restarts)
	}

	back, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != "the previous binary\n" {
		t.Errorf("exe after rollback = %q, want the previous binary restored", back)
	}
}
