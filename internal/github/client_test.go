package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAddDeployKeyIsReadOnly(t *testing.T) {
	var got map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/rdcstarr/tema/keys" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
			t.Errorf("Authorization = %q", auth)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 4242})
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	id, err := c.AddDeployKey(context.Background(), "rdcstarr/tema", "rec-deploy@server", "ssh-ed25519 AAAA")
	if err != nil {
		t.Fatalf("AddDeployKey: %v", err)
	}
	if id != 4242 {
		t.Errorf("id = %d, want 4242", id)
	}
	// A deploy key that can push is a deploy key that can be abused.
	if got["read_only"] != true {
		t.Errorf("read_only = %v, want true", got["read_only"])
	}
}

func TestCreateHookSendsSecretAndPushOnly(t *testing.T) {
	var got map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 77})
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	id, err := c.CreateHook(context.Background(), "rdcstarr/tema", "http://1.2.3.4:9000/hook/abc", "s3cret")
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	if id != 77 {
		t.Errorf("id = %d, want 77", id)
	}

	events, _ := got["events"].([]any)
	if len(events) != 1 || events[0] != "push" {
		t.Errorf("events = %v, want [push]", events)
	}

	cfg, _ := got["config"].(map[string]any)
	if cfg["secret"] != "s3cret" || cfg["url"] != "http://1.2.3.4:9000/hook/abc" || cfg["content_type"] != "json" {
		t.Errorf("config = %v", cfg)
	}
}

func TestUserReadsScopesFromTheHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo, admin:repo_hook")
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "rdcstarr"})
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	u, err := c.User(context.Background())
	if err != nil {
		t.Fatalf("User: %v", err)
	}
	if u.Login != "rdcstarr" {
		t.Errorf("Login = %q", u.Login)
	}
	if len(MissingScopes(u.Scopes)) != 0 {
		t.Errorf("Scopes = %v, want repo and admin:repo_hook parsed", u.Scopes)
	}
}

func TestAPIErrorCarriesTheStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "Bad credentials"})
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	if _, err := c.User(context.Background()); err == nil {
		t.Fatal("a 401 returned no error")
	}
}

// A 4xx is permanent: retrying a rejected token only burns the rate limit. A
// 5xx is transient and must be retried, or a flaky GitHub loses a deploy key.
func TestClientRetriesServerErrorsButNotClientErrors(t *testing.T) {
	var calls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 9})
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	id, err := c.AddDeployKey(context.Background(), "rdcstarr/tema", "rec-deploy@server", "ssh-ed25519 AAAA")
	if err != nil {
		t.Fatalf("AddDeployKey: %v", err)
	}
	if id != 9 || calls != 3 {
		t.Errorf("id = %d after %d calls, want 9 after 3 (two 500s retried)", id, calls)
	}

	calls = 0
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer bad.Close()

	c.BaseURL = bad.URL
	if err := c.DeleteHook(context.Background(), "rdcstarr/tema", 77); err == nil {
		t.Fatal("a 404 returned no error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 — a 4xx must not be retried", calls)
	}
}

// A hook or key already gone on GitHub (deleted by hand, or by a previous
// attempt that GitHub applied but the client never confirmed) must be
// distinguishable from a real failure, so a caller can treat it as done.
func TestDeleteHookOn404IsErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	err := c.DeleteHook(context.Background(), "rdcstarr/tema", 77)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteHook on a 404 = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestDeleteDeployKeyOn404IsErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	err := c.DeleteDeployKey(context.Background(), "rdcstarr/tema", 4242)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteDeployKey on a 404 = %v, want errors.Is(err, ErrNotFound)", err)
	}
}
