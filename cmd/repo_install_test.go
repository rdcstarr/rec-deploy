package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/ui"
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

// TestRepoMenuOptionsAreReachable pins the invariant this change makes
// structural: every entry the repo menu offers must resolve to a genuine
// child of `repo`. Before this change, four of the ten entries — deploy,
// rollback, scan, config — lived on the root instead, and repoDispatchFrom
// quietly redirected dispatch to walk from there so the split never showed up
// on screen. Now that the CLI mirrors the TUI hierarchy and that special case
// is gone, a menu entry added without a matching subcommand of repo fails
// here, rather than only the moment an operator picks it.
func TestRepoMenuOptionsAreReachable(t *testing.T) {
	root := newRootCmd()
	repo, _, err := root.Find([]string{"repo"})
	if err != nil {
		t.Fatalf("find the repo command: %v", err)
	}

	for _, option := range repoMenuOptions() {
		target, remaining, err := repo.Find([]string{option.Value})
		if err != nil {
			t.Errorf("repoMenuOptions offers %q: %v", option.Value, err)

			continue
		}
		if target == repo || len(remaining) != 0 {
			t.Errorf("repoMenuOptions offers %q, which does not resolve to a subcommand of repo", option.Value)
		}
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

// TestOfferFirstRepoKeepsTheScriptedError pins the guard that makes this helper
// safe to call from anywhere. Both call sites are behind isInteractive() today,
// so nothing reaches this branch in production — but the offer opens a prompt,
// and a prompt in a piped, CI or systemd run hangs on a terminal that is not
// there. The branch must stay, and it must keep reporting the dead end rather
// than backing out silently, which would look like success.
func TestOfferFirstRepoKeepsTheScriptedError(t *testing.T) {
	if isInteractive() {
		t.Skip("this test asserts the non-TTY branch; stdin is a terminal here")
	}

	err := offerFirstRepo(context.Background())
	if err == nil {
		t.Fatal("a non-interactive run returned no error for a server with no repository")
	}
	if errors.Is(err, ui.ErrBack) {
		t.Fatal("a non-interactive run backed out silently instead of reporting the dead end")
	}
	if !strings.Contains(err.Error(), "rec-deploy repo add") {
		t.Errorf("error %q does not point at the command that fixes it", err)
	}
}

// TestPickRepoOnAFreshServerDoesNotCrash covers the reported symptom: choosing
// deploy from the hub before any repository exists must not surface a raw
// error. Non-interactively pickRepo reports "no value, no error" so the command
// shows its help, which is the same contract it has always had.
func TestPickRepoOnAFreshServerDoesNotCrash(t *testing.T) {
	if isInteractive() {
		t.Skip("this test asserts the non-TTY branch; stdin is a terminal here")
	}

	slug, ok, err := pickRepo(context.Background(), nil, "Repository to deploy")
	if err != nil || ok || slug != "" {
		t.Fatalf("pickRepo(no args, no TTY) = (%q, %v, %v), want the caller to fall back to help", slug, ok, err)
	}
}
