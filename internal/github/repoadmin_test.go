package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewTokenAndSecretAreUniqueAndLong(t *testing.T) {
	seen := map[string]bool{}

	for i := 0; i < 100; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if len(tok) < 32 {
			t.Fatalf("token %q is too short to be unguessable", tok)
		}
		if strings.ContainsAny(tok, "/+= ") {
			t.Errorf("token %q is not URL-safe", tok)
		}
		if seen[tok] {
			t.Fatal("NewToken repeated a value")
		}
		seen[tok] = true
	}

	sec, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	if len(sec) < 32 {
		t.Errorf("secret %q is too short", sec)
	}
}

func TestHookURL(t *testing.T) {
	got, err := HookURL("http://1.2.3.4:9000", "abc")
	if err != nil {
		t.Fatalf("HookURL: %v", err)
	}
	if got != "http://1.2.3.4:9000/hook/abc" {
		t.Errorf("HookURL = %q", got)
	}

	if got, err := HookURL("http://1.2.3.4:9000/", "abc"); err != nil || got != "http://1.2.3.4:9000/hook/abc" {
		t.Errorf("HookURL with a trailing slash = %q, %v", got, err)
	}

	if _, err := HookURL("", "abc"); err == nil {
		t.Error("HookURL with no public_url succeeded")
	}
	if _, err := HookURL("1.2.3.4:9000", "abc"); err == nil {
		t.Error("HookURL with no scheme succeeded")
	}
}

func TestValidateSlug(t *testing.T) {
	for _, ok := range []string{"rdcstarr/tema-mea", "octocat/Hello.World_1"} {
		if err := ValidateSlug(ok); err != nil {
			t.Errorf("ValidateSlug(%q) = %v, want nil", ok, err)
		}
	}

	// A slug goes straight into an API path (/repos/{slug}/keys) and into an SSH
	// URL, so anything but a plain owner/repo pair is rejected before it travels.
	for _, bad := range []string{
		"",
		"tema-mea",
		"rdcstarr/tema/mea",
		"../../octocat/Hello-World",
		"rdcstarr/tema mea",
		"rdcstarr/",
		"/tema-mea",
		"rdcstarr/tema-mea?x=1",
	} {
		if err := ValidateSlug(bad); err == nil {
			t.Errorf("ValidateSlug(%q) succeeded, want an error", bad)
		}
	}
}

func TestUpdateHookSendsTheWholeConfig(t *testing.T) {
	var got map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/rdcstarr/tema/hooks/7" || r.Method != http.MethodPatch {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)

		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7})
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	if err := c.UpdateHook(context.Background(), "rdcstarr/tema", 7, "http://1.2.3.4:9000/hook/abc", "s3cret"); err != nil {
		t.Fatalf("UpdateHook: %v", err)
	}

	// GitHub replaces the hook's config wholesale, so a body carrying only the
	// rotated secret would blank the delivery URL and re-open insecure_ssl.
	cfg, _ := got["config"].(map[string]any)
	if cfg["secret"] != "s3cret" || cfg["url"] != "http://1.2.3.4:9000/hook/abc" ||
		cfg["content_type"] != "json" || cfg["insecure_ssl"] != "0" {
		t.Errorf("config = %v", cfg)
	}
}
