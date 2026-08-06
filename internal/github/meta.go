package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// MetaURL is GitHub's published metadata about itself. It is a var so the tests
// can point it at an httptest server.
var MetaURL = "https://api.github.com/meta"

// Meta is the part of GitHub's metadata rec-deploy reads: the SSH host keys a
// deploy pins instead of trusting whatever answers, and the address ranges
// GitHub delivers webhooks from, which is what a firewall has to allow.
type Meta struct {
	SSHKeys []string `json:"ssh_keys"`
	Hooks   []string `json:"hooks"`
}

// FetchMeta reads GitHub's metadata. It is unauthenticated because /meta needs
// no token, which is what lets a deploy pin host keys without one.
//
// Both fields are read live rather than hardcoded: GitHub rotates them, and a
// constant compiled into a binary that self-updates for months would eventually
// tell an operator to open the wrong ranges.
func FetchMeta(ctx context.Context) (Meta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, MetaURL, nil)
	if err != nil {
		return Meta{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Meta{}, fmt.Errorf("fetch github meta: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Meta{}, fmt.Errorf("fetch github meta: %s", resp.Status)
	}

	var meta Meta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return Meta{}, fmt.Errorf("decode github meta: %w", err)
	}

	return meta, nil
}
