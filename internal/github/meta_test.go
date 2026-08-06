package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveMeta points MetaURL at a stub /meta for the duration of the test.
func serveMeta(t *testing.T, status int, body string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	previous := MetaURL
	MetaURL = srv.URL
	t.Cleanup(func() { MetaURL = previous })
}

func TestFetchMetaReadsHostKeysAndHookRanges(t *testing.T) {
	serveMeta(t, http.StatusOK, `{
		"ssh_keys": ["ssh-ed25519 AAAAC3Nz"],
		"hooks": ["192.30.252.0/22", "2a0a:a440::/29"],
		"web": ["140.82.112.0/20"]
	}`)

	meta, err := FetchMeta(context.Background())
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}

	if len(meta.SSHKeys) != 1 || meta.SSHKeys[0] != "ssh-ed25519 AAAAC3Nz" {
		t.Errorf("SSHKeys = %q", meta.SSHKeys)
	}
	// The hook ranges are what a firewall has to allow, and they are read live
	// because GitHub rotates them.
	if len(meta.Hooks) != 2 || meta.Hooks[0] != "192.30.252.0/22" || meta.Hooks[1] != "2a0a:a440::/29" {
		t.Errorf("Hooks = %q", meta.Hooks)
	}
}

func TestFetchMetaRejectsANonOKResponse(t *testing.T) {
	serveMeta(t, http.StatusServiceUnavailable, `{}`)

	if _, err := FetchMeta(context.Background()); err == nil {
		t.Fatal("FetchMeta succeeded on a 503")
	}
}
