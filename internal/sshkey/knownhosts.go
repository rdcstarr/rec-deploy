package sshkey

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// metaURL is GitHub's published metadata, including its current SSH host keys.
// It is a var so the tests can point it at an httptest server.
var metaURL = "https://api.github.com/meta"

// FetchHostKeys returns github.com's current SSH host keys, straight from
// GitHub. Pinning these is what replaces the old implementation's
// StrictHostKeyChecking=no, which verifies nothing.
func FetchHostKeys(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch github host keys: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch github host keys: %s", resp.Status)
	}

	var meta struct {
		SSHKeys []string `json:"ssh_keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, fmt.Errorf("decode github meta: %w", err)
	}
	if len(meta.SSHKeys) == 0 {
		return nil, fmt.Errorf("github returned no ssh host keys")
	}

	return meta.SSHKeys, nil
}

// WriteKnownHosts pins github.com's host keys into path. The file is public
// data, so it is world-readable: the site user's git reads it.
func WriteKnownHosts(ctx context.Context, path string) error {
	keys, err := FetchHostKeys(ctx)
	if err != nil {
		return err
	}

	var b strings.Builder
	for _, k := range keys {
		b.WriteString("github.com " + k + "\n")
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write known_hosts: %w", err)
	}

	// WriteFile applies the umask on create and leaves an existing file's mode
	// alone; git runs as the site user and must be able to read this.
	return os.Chmod(path, 0o644)
}

// GitSSHCommand builds the GIT_SSH_COMMAND a deploy runs git with: the host key
// comes from the pinned known_hosts, the deploy key from the ephemeral agent
// listening on socket.
//
// An empty socket pins the host key without offering a key, which is what a
// repository with no key on this server needs: without GIT_SSH_COMMAND at all,
// git would fall back to the site user's ssh config and known_hosts — files this
// tool does not control, and which on a shared box may well carry
// StrictHostKeyChecking=no. These options are given on the command line, where
// ssh takes them ahead of any ~/.ssh/config.
func GitSSHCommand(socket, knownHosts string) string {
	opts := []string{
		"ssh",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "BatchMode=yes",
	}
	if socket != "" {
		opts = append(opts, "-o", "IdentityAgent="+socket, "-o", "IdentitiesOnly=no")
	}

	return strings.Join(opts, " ")
}
