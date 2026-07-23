// Package sshkey owns rec-deploy's per-repository ed25519 deploy keys: generation,
// root-only storage, the ephemeral in-process agent that lends a key to a deploy
// without ever writing it into the site user's home, and github.com's pinned
// host keys.
package sshkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Key is a passphrase-less ed25519 deploy key pair.
type Key struct {
	// Private is the OpenSSH-format private key. It is never printed, never
	// logged, and never copied into a site user's home.
	Private []byte
	// Public is the authorized_keys line uploaded to GitHub.
	Public string
}

// Generate creates a passphrase-less ed25519 key pair carrying comment.
func Generate(comment string) (Key, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Key{}, fmt.Errorf("generate ed25519 key: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return Key{}, fmt.Errorf("marshal private key: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return Key{}, fmt.Errorf("marshal public key: %w", err)
	}

	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment

	return Key{Private: pem.EncodeToMemory(block), Public: line}, nil
}
