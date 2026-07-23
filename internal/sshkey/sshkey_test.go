package sshkey

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// serveMeta points metaURL at a stub /meta for the duration of the test.
func serveMeta(t *testing.T, body string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	previous := metaURL
	metaURL = srv.URL
	t.Cleanup(func() { metaURL = previous })
}

func TestGenerateProducesAnEd25519Pair(t *testing.T) {
	k, err := Generate("rec-deploy:rdcstarr/tema")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !strings.HasPrefix(k.Public, "ssh-ed25519 ") {
		t.Errorf("Public = %q, want an ssh-ed25519 key", k.Public)
	}
	if !strings.Contains(k.Public, "rec-deploy:rdcstarr/tema") {
		t.Errorf("Public = %q, want the comment", k.Public)
	}

	signer, err := ssh.ParsePrivateKey(k.Private)
	if err != nil {
		t.Fatalf("ParsePrivateKey: %v — the key must be passphrase-less", err)
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Errorf("private key type = %s, want %s", signer.PublicKey().Type(), ssh.KeyAlgoED25519)
	}
}

func TestSaveIsOwnerReadOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")

	k, err := Generate("rec-deploy:rdcstarr/tema")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	path, err := Save(dir, "rdcstarr/tema", k)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o400 {
		t.Errorf("key perm = %o, want 400", perm)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("keys dir perm = %o, want 700", perm)
	}

	loaded, err := Load(dir, "rdcstarr/tema")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(loaded.Private) != string(k.Private) || loaded.Public != k.Public {
		t.Error("Load did not round-trip the key")
	}

	if err := Remove(dir, "rdcstarr/tema"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := Load(dir, "rdcstarr/tema"); err == nil {
		t.Error("Load after Remove succeeded")
	}
}

// Save overwrites a 0400 key in place: rotation must not trip over the old key's
// permissions.
func TestSaveOverwritesAnExistingKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")

	first, err := Generate("rec-deploy:rdcstarr/tema")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := Save(dir, "rdcstarr/tema", first); err != nil {
		t.Fatalf("Save: %v", err)
	}

	second, err := Generate("rec-deploy:rdcstarr/tema")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := Save(dir, "rdcstarr/tema", second); err != nil {
		t.Fatalf("Save (rotate): %v", err)
	}

	loaded, err := Load(dir, "rdcstarr/tema")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(loaded.Private) != string(second.Private) {
		t.Error("Save did not replace the previous key")
	}
}

// The key is served over a socket, never written into the site user's ~/.ssh.
func TestStartAgentServesTheKey(t *testing.T) {
	k, err := Generate("rec-deploy:rdcstarr/tema")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	a, err := StartAgent(k.Private, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	conn, err := net.Dial("unix", a.Socket())
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	defer func() { _ = conn.Close() }()

	keys, err := agent.NewClient(conn).List()
	if err != nil {
		t.Fatalf("agent List: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("agent serves %d keys, want 1", len(keys))
	}
	if keys[0].Type() != ssh.KeyAlgoED25519 {
		t.Errorf("agent key type = %s", keys[0].Type())
	}

	socket := a.Socket()
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(socket); err == nil {
		t.Error("the agent socket outlived Close — it must be destroyed with the deploy")
	}
	if err := a.Close(); err != nil {
		t.Errorf("second Close: %v, want nil — Close is idempotent", err)
	}
}

func TestWriteKnownHostsPinsGitHubsKeys(t *testing.T) {
	const hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
	serveMeta(t, `{"ssh_keys":["`+hostKey+`"],"hooks":["192.30.252.0/22"]}`)

	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := WriteKnownHosts(context.Background(), path); err != nil {
		t.Fatalf("WriteKnownHosts: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if want := "github.com " + hostKey + "\n"; string(b) != want {
		t.Errorf("known_hosts = %q, want %q", b, want)
	}

	// The site user's git reads this file, so it must be world-readable.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Errorf("known_hosts perm = %o, want 644", perm)
	}

	// Parsing it back is the real proof it is a usable known_hosts line.
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey)); err != nil {
		t.Fatalf("the pinned line is not a valid host key: %v", err)
	}
}

// Pinning fails closed: no keys means no known_hosts, not an empty one.
func TestWriteKnownHostsFailsWhenGitHubReturnsNoKeys(t *testing.T) {
	serveMeta(t, `{"ssh_keys":[]}`)

	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := WriteKnownHosts(context.Background(), path); err == nil {
		t.Fatal("WriteKnownHosts succeeded with zero host keys")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("WriteKnownHosts wrote a known_hosts file despite having no keys")
	}
}

func TestGitSSHCommandPinsTheHostKey(t *testing.T) {
	got := GitSSHCommand("/run/rec-deploy/agent.sock", "/var/lib/rec-deploy/known_hosts")

	if strings.Contains(got, "StrictHostKeyChecking=no") {
		t.Fatalf("GitSSHCommand = %q — StrictHostKeyChecking=no is forbidden", got)
	}
	for _, want := range []string{
		"StrictHostKeyChecking=yes",
		"UserKnownHostsFile=/var/lib/rec-deploy/known_hosts",
		"IdentityAgent=/run/rec-deploy/agent.sock",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("GitSSHCommand = %q, missing %q", got, want)
		}
	}
}

// A repository with no key on this server still deploys, and its host key is
// still pinned: without GIT_SSH_COMMAND, git would read the site user's
// ~/.ssh/config and known_hosts instead — files this tool does not control, and
// which on a shared box may carry StrictHostKeyChecking=no.
func TestGitSSHCommandPinsTheHostKeyWithNoAgent(t *testing.T) {
	got := GitSSHCommand("", "/var/lib/rec-deploy/known_hosts")

	if strings.Contains(got, "StrictHostKeyChecking=no") {
		t.Fatalf("GitSSHCommand = %q — StrictHostKeyChecking=no is forbidden", got)
	}
	for _, want := range []string{
		"StrictHostKeyChecking=yes",
		"UserKnownHostsFile=/var/lib/rec-deploy/known_hosts",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("GitSSHCommand = %q, missing %q", got, want)
		}
	}
	// No agent to point at: an empty IdentityAgent would disable agent lookup in a
	// way that reads as a bug, and offering the socket path of nothing is worse.
	if strings.Contains(got, "IdentityAgent") {
		t.Errorf("GitSSHCommand = %q, must not name an agent when there is none", got)
	}
}
