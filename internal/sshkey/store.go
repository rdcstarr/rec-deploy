package sshkey

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Path returns the on-disk location of a repository's private key.
func Path(dir, repository string) string {
	return filepath.Join(dir, strings.ReplaceAll(repository, "/", "_"))
}

// Save writes the key pair root-only: the directory 0700, the private key 0400,
// the public key 0644 beside it.
func Save(dir, repository string, k Key) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create keys dir: %w", err)
	}
	// MkdirAll applies the umask, and an existing dir keeps its old mode.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("restrict keys dir: %w", err)
	}

	path := Path(dir, repository)

	// Remove first: an existing key is 0400 and cannot be truncated in place.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("replace deploy key: %w", err)
	}
	if err := os.WriteFile(path, k.Private, 0o400); err != nil {
		return "", fmt.Errorf("write deploy key: %w", err)
	}
	if err := os.WriteFile(path+".pub", []byte(k.Public+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("write public key: %w", err)
	}

	return path, nil
}

// Load reads a repository's key pair.
func Load(dir, repository string) (Key, error) {
	path := Path(dir, repository)

	priv, err := os.ReadFile(path)
	if err != nil {
		return Key{}, fmt.Errorf("read deploy key for %s: %w — run `rec-deploy repo add %s`", repository, err, repository)
	}

	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		return Key{}, fmt.Errorf("read public key for %s: %w", repository, err)
	}

	return Key{Private: priv, Public: strings.TrimSpace(string(pub))}, nil
}

// Remove deletes a repository's key pair.
func Remove(dir, repository string) error {
	path := Path(dir, repository)

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove deploy key for %s: %w", repository, err)
	}
	if err := os.Remove(path + ".pub"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove public key for %s: %w", repository, err)
	}

	return nil
}
