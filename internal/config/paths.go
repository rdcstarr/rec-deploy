// Package config resolves rec-deploy's configuration and state locations and loads
// its typed settings, layering environment overrides on top of the defaults.
//
// As root it uses the FHS layout the .deb/.rpm packages install into
// (/etc/rec-deploy, /var/lib/rec-deploy); otherwise — development, or an operator
// poking at the CLI without sudo — it falls back to the XDG config directory.
package config

import (
	"os"
	"path/filepath"
)

// dirName is the program's subdirectory under the user's config home.
const dirName = "rec-deploy"

// isRoot reports whether the process runs with root's effective UID, which is
// what selects the FHS layout over the XDG fallback.
func isRoot() bool { return os.Geteuid() == 0 }

// Dir returns the configuration directory: /etc/rec-deploy as root, otherwise
// $XDG_CONFIG_HOME/rec-deploy (falling back to ~/.config/rec-deploy).
func Dir() (string, error) {
	if isRoot() {
		return "/etc/rec-deploy", nil
	}

	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(base, dirName), nil
}

// File returns the path to the YAML config file.
func File() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "config.yaml"), nil
}

// StateDir returns the directory holding the database, the deploy keys and the
// lock files: /var/lib/rec-deploy as root, otherwise the config directory itself —
// a non-root run has no business writing under /var/lib.
func StateDir() (string, error) {
	if isRoot() {
		return "/var/lib/rec-deploy", nil
	}

	return Dir()
}

// StateDB returns the path to the SQLite state database.
func StateDB() (string, error) { return underState("state.db") }

// MCPDir returns the root-only directory holding Cloudflare Tunnel runtime
// state and its verified helper binary.
func MCPDir() (string, error) { return underState("mcp") }

// CloudflaredBinary returns the root-owned helper location. It sits outside the
// secret state directory so a systemd DynamicUser can execute it without being
// able to traverse or read the rest of rec-deploy's state.
func CloudflaredBinary() (string, error) {
	if isRoot() {
		return "/usr/local/lib/rec-deploy/cloudflared", nil
	}
	dir, err := MCPDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bin", "cloudflared"), nil
}

// KeysDir returns the root-only directory holding the ed25519 deploy keys.
func KeysDir() (string, error) { return underState("keys") }

// LocksDir returns the directory holding the per-path advisory deploy locks.
func LocksDir() (string, error) { return underState("locks") }

// KnownHostsFile returns the path to the known_hosts file holding github.com's
// pinned host keys. It is world-readable: the site user's git runs against it.
func KnownHostsFile() (string, error) { return underState("known_hosts") }

// underState joins name onto the state directory.
func underState(name string) (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, name), nil
}
