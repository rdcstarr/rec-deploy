package config

import (
	"path/filepath"
	"testing"
)

func TestPathsNonRoot(t *testing.T) {
	if isRoot() {
		t.Skip("running as root: the XDG fallback is not in effect")
	}

	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")

	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := filepath.Join("/tmp/xdg", "rec-deploy"); dir != want {
		t.Errorf("Dir = %q, want %q", dir, want)
	}

	// Non-root keeps state alongside the config, not in /var/lib.
	state, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if state != dir {
		t.Errorf("StateDir = %q, want %q", state, dir)
	}

	db, err := StateDB()
	if err != nil {
		t.Fatalf("StateDB: %v", err)
	}
	if want := filepath.Join(dir, "state.db"); db != want {
		t.Errorf("StateDB = %q, want %q", db, want)
	}

	keys, err := KeysDir()
	if err != nil {
		t.Fatalf("KeysDir: %v", err)
	}
	if want := filepath.Join(dir, "keys"); keys != want {
		t.Errorf("KeysDir = %q, want %q", keys, want)
	}

	locks, err := LocksDir()
	if err != nil {
		t.Fatalf("LocksDir: %v", err)
	}
	if want := filepath.Join(dir, "locks"); locks != want {
		t.Errorf("LocksDir = %q, want %q", locks, want)
	}

	known, err := KnownHostsFile()
	if err != nil {
		t.Fatalf("KnownHostsFile: %v", err)
	}
	if want := filepath.Join(dir, "known_hosts"); known != want {
		t.Errorf("KnownHostsFile = %q, want %q", known, want)
	}

	file, err := File()
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if want := filepath.Join(dir, "config.yaml"); file != want {
		t.Errorf("File = %q, want %q", file, want)
	}
}
