package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCloneOwnerDirMissingDestinationUsesParent(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "checkout")

	got, err := cloneOwnerDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != parent {
		t.Fatalf("owner dir = %q, want %q", got, parent)
	}
}

func TestCloneOwnerDirExistingEmptyDestinationUsesDestination(t *testing.T) {
	path := t.TempDir()

	got, err := cloneOwnerDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("owner dir = %q, want %q", got, path)
	}
}

func TestCloneOwnerDirRejectsNonEmptyDestination(t *testing.T) {
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "index.html"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := cloneOwnerDir(path)
	if !errors.Is(err, errCloneDestinationNotEmpty) {
		t.Fatalf("error = %v, want %v", err, errCloneDestinationNotEmpty)
	}
}

func TestCloneOwnerDirRejectsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := cloneOwnerDir(path)
	if !errors.Is(err, errCloneDestinationNotEmpty) {
		t.Fatalf("error = %v, want %v", err, errCloneDestinationNotEmpty)
	}
}

func TestCloneOwnerDirRejectsSymlink(t *testing.T) {
	target := t.TempDir()
	path := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	_, err := cloneOwnerDir(path)
	if !errors.Is(err, errCloneDestinationSymlink) {
		t.Fatalf("error = %v, want %v", err, errCloneDestinationSymlink)
	}
}

func TestCloneDestinationEntriesIncludesHiddenDirectories(t *testing.T) {
	path := t.TempDir()
	if err := os.Mkdir(filepath.Join(path, ".well-known"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := cloneDestinationEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "• .well-known/" {
		t.Fatalf("entries = %#v", got)
	}
}

func TestClearDirectoryContentsPreservesRootAndDoesNotFollowSymlink(t *testing.T) {
	path := t.TempDir()
	target := t.TempDir()
	targetFile := filepath.Join(target, "keep")
	if err := os.WriteFile(targetFile, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, ".hidden"), []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(path, "linked")); err != nil {
		t.Fatal(err)
	}

	if err := clearDirectoryContents(path); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("root directory was removed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("destination still contains %#v", entries)
	}
	if _, err := os.Stat(targetFile); err != nil {
		t.Fatalf("symlink target was modified: %v", err)
	}
}
