package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/discover"
	"github.com/rdcstarr/rec-deploy/internal/store"
	"github.com/rdcstarr/rec-deploy/internal/units"
)

// TestScanRowBroken is the contract of `rec-deploy scan`: a checkout whose manifest
// will not parse is still listed, with ✗ and the parse error. Dropping it is the
// an old implementation defect — the operator is looking for exactly that row.
func TestScanRowBroken(t *testing.T) {
	row := scanRow(discover.Installation{
		Path: "/var/www/api",
		User: "root",
		Err:  errors.New("yaml: line 3: mapping values are not allowed"),
	})

	if row[0] != "/var/www/api" {
		t.Errorf("path column = %q, want /var/www/api", row[0])
	}
	if !strings.Contains(row[1], "✗ yaml: line 3") {
		t.Errorf("row %q does not carry the parse error", row[1])
	}
}

// TestScanRowMarkers covers every marker an operator must see rather than
// discover later: a root-owned target, an https origin that cannot use the
// deploy key, and a tree with stray ownership.
func TestScanRowMarkers(t *testing.T) {
	row := scanRow(discover.Installation{
		Path:         "/var/www/api",
		Repository:   "rdcstarr/api",
		Branch:       "main",
		User:         "root",
		RanAsRoot:    true,
		RemoteHTTPS:  true,
		Inconsistent: "/var/www/api/cache/x",
	})

	for _, want := range []string{"rdcstarr/api", "main", "root", "⚠ root", "⚠ https", "⚠ mixed (/var/www/api/cache/x)"} {
		if !strings.Contains(row[1], want) {
			t.Errorf("row %q is missing %q", row[1], want)
		}
	}
}

// TestHealthURL covers the address status probes. A wildcard bind is not an
// address to connect to: probing 0.0.0.0 or a bare :9000 must land on the
// loopback the daemon is also listening on.
func TestHealthURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"public url wins", config.Config{PublicURL: "http://1.2.3.4:9000", Listen: "0.0.0.0:9000"}, "http://1.2.3.4:9000/health"},
		{"public url trailing slash", config.Config{PublicURL: "http://1.2.3.4:9000/"}, "http://1.2.3.4:9000/health"},
		{"wildcard bind", config.Config{Listen: "0.0.0.0:9000"}, "http://127.0.0.1:9000/health"},
		{"bare port", config.Config{Listen: ":9000"}, "http://127.0.0.1:9000/health"},
		{"ipv6 wildcard", config.Config{Listen: "[::]:9000"}, "http://127.0.0.1:9000/health"},
		{"explicit host", config.Config{Listen: "127.0.0.1:9001"}, "http://127.0.0.1:9001/health"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := healthURL(&tt.cfg); got != tt.want {
				t.Errorf("healthURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusOverviewPrioritizesProblems(t *testing.T) {
	out := capture(t, func() {
		renderStatusOverview(false, "http://127.0.0.1:9000/health", true, false,
			[]units.Status{{Unit: "rec-deploy.service", State: units.StateStale}},
			nil,
			[]store.DeployPath{{Path: "/srv/app", Status: store.StatusFailed}},
		)
	})

	attention := strings.Index(out, "needs attention")
	healthy := strings.Index(out, "healthy")
	if attention < 0 {
		t.Fatalf("status has no needs-attention section:\n%s", out)
	}
	if healthy >= 0 && attention > healthy {
		t.Errorf("healthy section appears before problems:\n%s", out)
	}
	for _, want := range []string{"daemon not answering", "differs from this version", "no repository registered", "1 installation needs attention"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}

// TestParseCommands reads the per-command results back out of the JSON column
// `rec-deploy logs --path` renders. A row written by an older or crashed run can
// hold anything, and an unreadable column must not lose the deploy's status.
func TestParseCommands(t *testing.T) {
	cmds := parseCommands(`[{"command":"composer install","exit_code":1,"duration":1500000000,"output":"boom\n","timed_out":false}]`)
	if len(cmds) != 1 {
		t.Fatalf("parseCommands returned %d commands, want 1", len(cmds))
	}
	if cmds[0].Command != "composer install" || cmds[0].ExitCode != 1 {
		t.Errorf("command = %+v, want composer install exit 1", cmds[0])
	}
	if cmds[0].Duration != 1500*time.Millisecond {
		t.Errorf("duration = %s, want 1.5s", cmds[0].Duration)
	}

	if got := parseCommands(""); got != nil {
		t.Errorf("parseCommands(\"\") = %v, want nil", got)
	}
	if got := parseCommands("not json"); got != nil {
		t.Errorf("parseCommands(garbage) = %v, want nil", got)
	}
}

// TestFindPath picks one checkout's result out of a deploy that fanned out over
// several.
func TestFindPath(t *testing.T) {
	paths := []store.DeployPath{
		{Path: "/srv/prod", Status: store.StatusSuccess},
		{Path: "/srv/staging", Status: store.StatusFailed},
	}

	p, ok := findPath(paths, "/srv/staging")
	if !ok {
		t.Fatal("findPath did not find /srv/staging")
	}
	if p.Status != store.StatusFailed {
		t.Errorf("status = %q, want failed", p.Status)
	}

	if _, ok := findPath(paths, "/srv/other"); ok {
		t.Error("findPath invented a result for a path the deploy never touched")
	}
}

// TestPathLogPicksTheLastDeployOfThePath runs against a real database: `logs
// --path` must report the newest deploy that touched that checkout, not the
// newest deploy overall — a repository whose latest push only redeployed another
// path would otherwise hide the failure the operator is looking for.
func TestPathLogPicksTheLastDeployOfThePath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx := context.Background()

	st, err := openStore(ctx)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer func() { _ = st.Close() }()

	repoID, err := st.RepoInsert(ctx, store.Repo{Repository: "rdcstarr/api", Token: "t", Secret: "s"})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	// Oldest to newest: /srv/prod succeeded, then failed, then a deploy that
	// touched only /srv/other.
	seed := []struct {
		path, status, output string
	}{
		{"/srv/prod", store.StatusSuccess, "old output"},
		{"/srv/prod", store.StatusFailed, "boom"},
		{"/srv/other", store.StatusSuccess, "unrelated"},
	}
	for _, s := range seed {
		id, err := st.DeployStart(ctx, store.Deploy{RepoID: repoID, Status: store.StatusRunning})
		if err != nil {
			t.Fatalf("DeployStart: %v", err)
		}
		if err := st.DeployPathInsert(ctx, store.DeployPath{
			DeployID: id,
			Path:     s.path,
			User:     "deploy",
			Status:   s.status,
			Commands: `[{"command":"composer install","exit_code":1,"duration":1000000000,"output":"` + s.output + `"}]`,
		}); err != nil {
			t.Fatalf("DeployPathInsert: %v", err)
		}
		if err := st.DeployFinish(ctx, id, s.status); err != nil {
			t.Fatalf("DeployFinish: %v", err)
		}
	}

	out := capture(t, func() {
		if err := pathLog(ctx, "", "/srv/prod"); err != nil {
			t.Fatalf("pathLog: %v", err)
		}
	})

	if !strings.Contains(out, "boom") {
		t.Errorf("pathLog output does not show the last deploy of /srv/prod:\n%s", out)
	}
	if strings.Contains(out, "old output") {
		t.Errorf("pathLog showed a superseded deploy:\n%s", out)
	}
	if strings.Contains(out, "unrelated") {
		t.Errorf("pathLog showed a deploy of another path:\n%s", out)
	}

	if err := pathLog(ctx, "", "/srv/never"); err == nil {
		t.Error("pathLog invented a deploy for a path nothing ever deployed")
	}

	// The history itself: every deploy, under the slug its repo_id resolves to.
	history := capture(t, func() {
		if err := listLogs(ctx, "", 20); err != nil {
			t.Fatalf("listLogs: %v", err)
		}
	})
	if n := strings.Count(history, "rdcstarr/api"); n != len(seed) {
		t.Errorf("history names the repository %d times, want %d:\n%s", n, len(seed), history)
	}
}

// capture runs f with stdout redirected to a pipe and returns what it printed —
// the ui package writes straight to os.Stdout.
func capture(t *testing.T, f func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	stdout := os.Stdout
	os.Stdout = w
	f()
	os.Stdout = stdout
	_ = w.Close()

	var b strings.Builder
	if _, err := io.Copy(&b, r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}

	return b.String()
}

// TestStatusMenuOffersScanAndOneLifecycleAction pins that the status screen's
// actions always include scan, and offer exactly the service action that makes
// sense for the daemon's current state — never both start and stop.
func TestStatusMenuOffersScanAndOneLifecycleAction(t *testing.T) {
	seen := make(map[string]bool)
	for _, option := range statusMenuOptions(context.Background()) {
		seen[option.Value] = true
	}

	if !seen["scan"] {
		t.Errorf("status actions do not include scan: %v", seen)
	}
	if !seen["back"] {
		t.Errorf("status actions have no way back: %v", seen)
	}
	if seen["start"] && seen["stop"] {
		t.Errorf("status actions offer both start and stop: %v", seen)
	}
}

// TestDeployRow renders one history line: when it ran, against what it did.
func TestDeployRow(t *testing.T) {
	row := deployRow(store.Deploy{
		Ref:       "refs/heads/main",
		SHA:       "0123456789abcdef",
		Message:   "fix the thing\n\nwith a body nobody wants in a table",
		Author:    "rdcstarr",
		Status:    store.StatusSuccess,
		StartedAt: time.Date(2026, 7, 14, 10, 30, 0, 0, time.UTC),
	}, "rdcstarr/api")

	if row[0] != "2026-07-14 10:30:00" {
		t.Errorf("time column = %q", row[0])
	}
	for _, want := range []string{"success", "rdcstarr/api", "0123456", "main", "fix the thing"} {
		if !strings.Contains(row[1], want) {
			t.Errorf("row %q is missing %q", row[1], want)
		}
	}
	if strings.Contains(row[1], "\n") {
		t.Errorf("row %q spills the commit body over several lines", row[1])
	}
}
