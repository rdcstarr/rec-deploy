// Package manifest reads .rec-deploy.yml or .rec-deploy.yaml from the root of a
// deployable repository. It is read after the pull, so the deploy steps version
// with the code.
//
// A missing or invalid manifest is a hard failure. an old implementation rescues the
// parse into an empty command list and then reports success having run nothing;
// that silent no-op is the defect this package exists to make impossible. A
// valid manifest that declares no steps is a different thing entirely — an
// explicit "pull the code, run nothing" — and is accepted.
package manifest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Filename is the preferred manifest name.
const Filename = ".rec-deploy.yml"

// AlternateFilename is the supported long-extension spelling.
const AlternateFilename = ".rec-deploy.yaml"

// DefaultTimeout bounds a post_deploy step that does not set its own. It is
// generous on purpose: a cold `composer install` routinely outlives a minute.
const DefaultTimeout = 10 * time.Minute

// Manifest is a parsed rec-deploy YAML manifest.
type Manifest struct {
	// Repository is the owner/repo slug this checkout must belong to. It is
	// verified against `git remote get-url origin` before anything runs.
	Repository string `yaml:"repository"`
	// PostDeploy are the steps run, in order, after a successful git sync. An
	// empty list is valid: the sync alone is the deploy.
	PostDeploy []Step `yaml:"post_deploy"`
	// RollbackOnFailure resets the tree to the pre-deploy SHA and re-runs the
	// previous manifest's post_deploy when a step fails.
	RollbackOnFailure bool `yaml:"rollback_on_failure"`
}

// Step is one post_deploy command. In YAML it is either a bare string (the
// command) or an object with run, timeout and continue_on_failure.
type Step struct {
	// Run is the shell command.
	Run string
	// Timeout bounds the command; DefaultTimeout when unset.
	Timeout time.Duration
	// ContinueOnFailure lets the pipeline proceed past a non-zero exit.
	ContinueOnFailure bool
}

// stepObject is the object form of a Step, with the timeout still a string so
// the Go duration syntax ("15m") is parsed — and rejected — explicitly.
type stepObject struct {
	Run               string `yaml:"run"`
	Timeout           string `yaml:"timeout"`
	ContinueOnFailure bool   `yaml:"continue_on_failure"`
}

// UnmarshalYAML accepts both entry forms: a scalar is the command, a mapping is
// the full object.
func (s *Step) UnmarshalYAML(value *yaml.Node) error {
	s.Timeout = DefaultTimeout

	if value.Kind == yaml.ScalarNode {
		return value.Decode(&s.Run)
	}

	var obj stepObject
	if err := value.Decode(&obj); err != nil {
		return err
	}

	s.Run = obj.Run
	s.ContinueOnFailure = obj.ContinueOnFailure

	if obj.Timeout != "" {
		d, err := time.ParseDuration(obj.Timeout)
		if err != nil {
			return fmt.Errorf("step %q: invalid timeout %q — use a Go duration such as `15m`", obj.Run, obj.Timeout)
		}
		if d <= 0 {
			return fmt.Errorf("step %q: timeout must be positive, got %q", obj.Run, obj.Timeout)
		}
		s.Timeout = d
	}

	return nil
}

// Parse decodes and validates a manifest.
//
// Decoding is strict: an unknown top-level key is an error. A typo such as
// `post_deploys:` would otherwise leave the pipeline empty and the deploy would
// report success having run nothing — the defect this package exists to prevent.
func Parse(data []byte) (*Manifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Filename, err)
	}

	if err := m.validate(); err != nil {
		return nil, err
	}

	return &m, nil
}

// Load reads and validates the one supported manifest in dir. A missing file is
// an error: a deploy target without a manifest has nothing to run and must not
// be reported as a success.
func Load(dir string) (*Manifest, error) {
	path, err := resolvePath(dir)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w — commit a `%s` at the repository root", path, err, Filename)
	}

	m, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}

	return m, nil
}

// Exists reports whether dir contains either supported manifest filename.
func Exists(dir string) bool {
	for _, name := range []string{Filename, AlternateFilename} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// resolvePath selects the one manifest in dir and rejects ambiguous checkouts.
func resolvePath(dir string) (string, error) {
	short := filepath.Join(dir, Filename)
	long := filepath.Join(dir, AlternateFilename)
	_, shortErr := os.Stat(short)
	_, longErr := os.Stat(long)

	if shortErr == nil && longErr == nil {
		return "", fmt.Errorf("%s contains both %s and %s — keep only one manifest", dir, Filename, AlternateFilename)
	}
	if shortErr == nil {
		return short, nil
	}
	if longErr == nil {
		return long, nil
	}
	if !os.IsNotExist(shortErr) {
		return "", fmt.Errorf("inspect %s: %w", short, shortErr)
	}
	if !os.IsNotExist(longErr) {
		return "", fmt.Errorf("inspect %s: %w", long, longErr)
	}
	return short, nil
}

// validate enforces the invariants the deploy engine relies on.
func (m *Manifest) validate() error {
	if strings.TrimSpace(m.Repository) == "" {
		return fmt.Errorf("%s: `repository` is required — set it to the owner/repo slug", Filename)
	}
	owner, repo, ok := strings.Cut(m.Repository, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return fmt.Errorf("%s: `repository` must be an owner/repo slug, got %q", Filename, m.Repository)
	}

	for i, s := range m.PostDeploy {
		if strings.TrimSpace(s.Run) == "" {
			return fmt.Errorf("%s: post_deploy[%d] has no command — use a string or an object with `run`", Filename, i)
		}
	}

	return nil
}
