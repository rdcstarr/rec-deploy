package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const latestReleaseURL = "https://api.github.com/repos/cloudflare/cloudflared/releases/latest"

var downloadClient = &http.Client{Timeout: 5 * time.Minute}

type release struct {
	TagName string `json:"tag_name"`
	Body    string `json:"body"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Digest             string `json:"digest"`
	} `json:"assets"`
}

// InstallLatest downloads the official cloudflared binary and fails closed
// unless GitHub's release metadata supplies a matching SHA-256 digest.
func InstallLatest(ctx context.Context, dst string) (string, error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return "", fmt.Errorf("cloudflared provisioning supports linux amd64 and arm64")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := downloadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch cloudflared release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch cloudflared release: HTTP %d", resp.StatusCode)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&rel); err != nil {
		return "", fmt.Errorf("decode cloudflared release: %w", err)
	}
	name := "cloudflared-linux-" + runtime.GOARCH
	var assetURL, digest string
	for _, a := range rel.Assets {
		if a.Name == name {
			assetURL, digest = a.BrowserDownloadURL, strings.TrimPrefix(a.Digest, "sha256:")
			break
		}
	}
	if len(digest) != 64 {
		digest = digestFromReleaseBody(rel.Body, name)
	}
	if assetURL == "" || len(digest) != 64 {
		return "", fmt.Errorf("cloudflared %s has no verified %s asset", rel.TagName, name)
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	resp, err = downloadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download cloudflared: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download cloudflared: HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".cloudflared-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 200<<20)); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("download cloudflared: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != strings.ToLower(digest) {
		return "", fmt.Errorf("cloudflared checksum mismatch: expected %s, got %s", digest, got)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return "", fmt.Errorf("install cloudflared: %w", err)
	}
	return rel.TagName, nil
}

func digestFromReleaseBody(body, asset string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.Trim(strings.TrimSpace(line), "`")
		name, digest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != asset {
			continue
		}
		digest = strings.TrimSpace(digest)
		if len(digest) == 64 {
			if _, err := hex.DecodeString(digest); err == nil {
				return strings.ToLower(digest)
			}
		}
	}
	return ""
}
