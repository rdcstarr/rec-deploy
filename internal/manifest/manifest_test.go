package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAcceptsLongYAMLExtension(t *testing.T) {
	dir := t.TempDir()
	body := []byte("repository: owner/repo\npost_deploy:\n  - echo ready\n")
	if err := os.WriteFile(filepath.Join(dir, AlternateFilename), body, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repository != "owner/repo" || len(got.PostDeploy) != 1 {
		t.Fatalf("manifest = %#v", got)
	}
}

func TestLoadRejectsBothManifestFilenames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{Filename, AlternateFilename} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("repository: owner/repo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("error = %v, want ambiguous manifest error", err)
	}
}

func TestParseStringAndObjectEntries(t *testing.T) {
	m, err := Parse([]byte(`
repository: rdcstarr/tema-mea

post_deploy:
  - composer install --no-dev --optimize-autoloader
  - run: npm ci && npm run build
    timeout: 15m
  - run: wp cache flush
    continue_on_failure: true

rollback_on_failure: true
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if m.Repository != "rdcstarr/tema-mea" {
		t.Errorf("Repository = %q", m.Repository)
	}
	if !m.RollbackOnFailure {
		t.Error("RollbackOnFailure = false, want true")
	}
	if len(m.PostDeploy) != 3 {
		t.Fatalf("PostDeploy has %d steps, want 3", len(m.PostDeploy))
	}

	// A bare string entry: the command, the default timeout, fail-fast.
	if got := m.PostDeploy[0]; got.Run != "composer install --no-dev --optimize-autoloader" ||
		got.Timeout != DefaultTimeout || got.ContinueOnFailure {
		t.Errorf("step 0 = %+v, want default timeout %s and fail-fast", got, DefaultTimeout)
	}

	// This is the an old implementation defect: the timeout is parsed and then ignored,
	// so composer install dies at Laravel's 60s default. Here it must survive.
	if got := m.PostDeploy[1].Timeout; got != 15*time.Minute {
		t.Errorf("step 1 timeout = %s, want 15m", got)
	}

	if got := m.PostDeploy[2]; !got.ContinueOnFailure || got.Timeout != DefaultTimeout {
		t.Errorf("step 2 = %+v, want continue_on_failure with the default timeout", got)
	}
}

// An empty post_deploy is legitimate — a git-sync-only deploy. What must never
// be tolerated is a missing or malformed file silently yielding one.
func TestParseEmptyPostDeployIsValid(t *testing.T) {
	m, err := Parse([]byte("repository: rdcstarr/tema-mea\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.PostDeploy) != 0 {
		t.Errorf("PostDeploy = %v, want empty", m.PostDeploy)
	}
}

// An explicit but empty post_deploy list is the same intention, spelled out.
func TestParseExplicitEmptyPostDeployIsValid(t *testing.T) {
	m, err := Parse([]byte("repository: rdcstarr/tema-mea\npost_deploy: []\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.PostDeploy) != 0 {
		t.Errorf("PostDeploy = %v, want empty", m.PostDeploy)
	}
	if m.RollbackOnFailure {
		t.Error("RollbackOnFailure = true, want false by default")
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"missing repository":  "post_deploy:\n  - echo hi\n",
		"repository not slug": "repository: tema-mea\n",
		"malformed yaml":      "repository: [unterminated\n",
		"step with no run":    "repository: o/r\npost_deploy:\n  - timeout: 5m\n",
		"bad timeout":         "repository: o/r\npost_deploy:\n  - run: echo hi\n    timeout: soon\n",
		"empty step":          "repository: o/r\npost_deploy:\n  - \"\"\n",
		"zero timeout":        "repository: o/r\npost_deploy:\n  - run: echo hi\n    timeout: 0s\n",
		"unknown field":       "repository: o/r\npost_deploys:\n  - echo hi\n",
	}

	for name, src := range cases {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: Parse succeeded, want an error", name)
		}
	}
}

func TestLoadMissingFileIsAnError(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("Load of a directory with no .rec-deploy.yml succeeded, want a hard failure")
	}
	if !strings.Contains(err.Error(), Filename) {
		t.Errorf("error %q does not name %s", err, Filename)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %q does not unwrap to os.ErrNotExist", err)
	}
}

func TestLoadReadsTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte("repository: o/r\npost_deploy:\n  - echo hi\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.PostDeploy) != 1 || m.PostDeploy[0].Run != "echo hi" {
		t.Errorf("PostDeploy = %+v", m.PostDeploy)
	}
}

// A malformed manifest must fail loudly, and the error must say which file.
func TestLoadInvalidFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte("repository: [unterminated\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load of a malformed .rec-deploy.yml succeeded, want a hard failure")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error %q does not name the directory %s", err, dir)
	}
}
