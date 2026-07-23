package privexec

import (
	"context"
	"maps"
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	res, err := Run(context.Background(), "echo hello; echo oops >&2; exit 3", Options{Dir: t.TempDir()})
	if err == nil {
		t.Fatal("Run of a failing command returned nil error")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Output, "hello") || !strings.Contains(res.Output, "oops") {
		t.Errorf("Output = %q, want both stdout and stderr", res.Output)
	}
}

func TestRunRunsInDir(t *testing.T) {
	dir := t.TempDir()

	res, err := Run(context.Background(), "pwd", Options{Dir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// macOS symlinks /tmp; compare the suffix rather than the whole path.
	if !strings.Contains(res.Output, strings.TrimPrefix(dir, "/private")) {
		t.Errorf("Output = %q, want the command to run in %s", res.Output, dir)
	}
}

// The an old implementation defect, made impossible: the timeout is real, and it kills
// the whole process group — not just the shell.
func TestRunTimeoutKillsTheProcess(t *testing.T) {
	start := time.Now()

	res, err := Run(context.Background(), "sleep 30", Options{Dir: t.TempDir(), Timeout: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("Run of a timed-out command returned nil error")
	}
	if !res.TimedOut {
		t.Errorf("TimedOut = false, want true (err = %v)", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %s — the process was not killed", elapsed)
	}
}

func TestRunTimeoutKillsGrandchildren(t *testing.T) {
	dir := t.TempDir()
	marker := dir + "/alive"

	// The shell exits immediately; the backgrounded child would outlive it and
	// touch the marker unless the whole process group is killed.
	_, err := Run(context.Background(), "(sleep 1; touch "+marker+") & sleep 30", Options{
		Dir:     dir,
		Timeout: 200 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("want a timeout error")
	}

	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a grandchild survived the timeout — the process group was not killed")
	}
}

func TestRunStreamsWhileCapturing(t *testing.T) {
	var sink strings.Builder

	res, err := Run(context.Background(), "echo streamed", Options{Dir: t.TempDir(), Stream: &sink})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(sink.String(), "streamed") {
		t.Errorf("stream = %q, want the live output", sink.String())
	}
	if !strings.Contains(res.Output, "streamed") {
		t.Errorf("Output = %q, want the same output captured", res.Output)
	}
}

func TestRunOutputTail(t *testing.T) {
	res, err := Run(context.Background(), "yes abcdefghij | head -c 40000", Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Output) > TailBytes {
		t.Errorf("Output is %d bytes, want at most %d — only the tail is kept", len(res.Output), TailBytes)
	}
}

// multiGroupUser finds a user that actually has a supplementary group beyond its
// primary, and skips the test when the host has none.
//
// The target choice is the whole test. `nobody` — the obvious pick, and the one
// this file used — belongs to exactly one group, equal to its own Gid, so `want`
// and `got` are both one-element sets and the assertion holds even with the
// supplementary groups thrown away. A test that cannot fail is worse than no
// test: it reports coverage of the one rule CLAUDE.md calls non-negotiable while
// seeing nothing. Skipping loudly is honest; passing blind is not.
//
// os/user cannot enumerate accounts, so this tries the ones a Debian-family host
// conventionally ships with a second group — syslog is in adm, and so on.
func multiGroupUser(t *testing.T) *user.User {
	t.Helper()

	for _, name := range []string{"syslog", "games", "irc", "gnats", "list", "www-data", "daemon"} {
		u, err := user.Lookup(name)
		if err != nil {
			continue
		}
		// credential returns nil for the process's own user, by design.
		if u.Uid == strconv.Itoa(os.Geteuid()) {
			continue
		}
		if ids, err := u.GroupIds(); err == nil && len(ids) > 1 {
			return u
		}
	}

	t.Skip("no user on this host has a supplementary group — nothing here could prove the rule")

	return nil
}

// credential is where the non-negotiable rule lives — Uid, Gid AND the
// supplementary Groups, the ones sudo -u gets from PAM and SysProcAttr does not,
// whose loss silently breaks HestiaCP group memberships. Asserted here rather
// than only through `id -G` under root: `id -G` prints the primary group whether
// or not Groups was ever set, so it cannot see the omission this rule exists to
// prevent, and it skips on every unprivileged CI run besides. Reading /etc/group
// needs no privileges, so the rule is pinned wherever the suite runs.
func TestCredentialCarriesTheSupplementaryGroups(t *testing.T) {
	target := multiGroupUser(t)

	cred, err := credential(target)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if cred == nil {
		t.Fatal("credential = nil for another user — the process would keep its own privileges")
	}

	if got := strconv.Itoa(int(cred.Uid)); got != target.Uid {
		t.Errorf("Uid = %s, want %s", got, target.Uid)
	}
	if got := strconv.Itoa(int(cred.Gid)); got != target.Gid {
		t.Errorf("Gid = %s, want %s", got, target.Gid)
	}

	ids, err := target.GroupIds()
	if err != nil {
		t.Fatalf("GroupIds: %v", err)
	}

	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	got := map[string]bool{}
	for _, g := range cred.Groups {
		got[strconv.Itoa(int(g))] = true
	}

	if !maps.Equal(got, want) {
		t.Errorf("Groups = %v, want exactly %v — SysProcAttr does not inherit these from PAM, so a\ndropped or partial set is a silent loss of group membership", got, want)
	}
}

// Privilege drop needs root. The rule it enforces — Uid, Gid AND the
// supplementary Groups — is the one sudo -u gets from PAM and SysProcAttr does
// not, and dropping it silently breaks HestiaCP group memberships.
func TestRunDropsPrivileges(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("privilege drop needs root")
	}

	target := multiGroupUser(t)

	res, err := Run(context.Background(), "id -u; id -g; echo $HOME", Options{Dir: "/tmp", User: target})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	lines := strings.Fields(res.Output)
	if len(lines) < 3 {
		t.Fatalf("Output = %q", res.Output)
	}
	if lines[0] != target.Uid {
		t.Errorf("uid = %s, want %s", lines[0], target.Uid)
	}
	if lines[1] != target.Gid {
		t.Errorf("gid = %s, want %s", lines[1], target.Gid)
	}
	if lines[2] != target.HomeDir {
		t.Errorf("HOME = %s, want %s (the real passwd entry, not /home/%s)", lines[2], target.HomeDir, target.Username)
	}

	groups, err := target.GroupIds()
	if err != nil {
		t.Fatalf("GroupIds: %v", err)
	}
	got, err := Run(context.Background(), "id -G", Options{Dir: "/tmp", User: target})
	if err != nil {
		t.Fatalf("Run id -G: %v", err)
	}

	// Set equality, not Contains: `id -G` prints the primary group regardless, so
	// a substring check for a user whose only group is its primary passes even
	// with Groups never set. The construction of the set is pinned without root by
	// TestCredentialCarriesTheSupplementaryGroups; this proves the kernel applies
	// it.
	want := map[string]bool{}
	for _, gid := range groups {
		want[gid] = true
	}
	have := map[string]bool{}
	for _, gid := range strings.Fields(got.Output) {
		have[gid] = true
	}

	if !maps.Equal(have, want) {
		t.Errorf("`id -G` = %v, want exactly %v", have, want)
	}
}
