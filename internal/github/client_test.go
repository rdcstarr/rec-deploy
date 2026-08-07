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

func TestCreateHookSendsSecretAndBothEvents(t *testing.T) {
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
	if len(events) != 2 || events[0] != "push" || events[1] != "repository_dispatch" {
		t.Errorf("events = %v, want [push repository_dispatch]", events)
	}

	cfg, _ := got["config"].(map[string]any)
	if cfg["secret"] != "s3cret" || cfg["url"] != "http://1.2.3.4:9000/hook/abc" || cfg["content_type"] != "json" {
		t.Errorf("config = %v", cfg)
	}
}

func TestDispatchSendsTheEventTypeAndBranch(t *testing.T) {
	var got map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/rdcstarr/tema/dispatches" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	if err := c.Dispatch(context.Background(), "rdcstarr/tema", DispatchSetup, "develop"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got["event_type"] != DispatchSetup {
		t.Errorf("event_type = %v", got["event_type"])
	}
	payload, _ := got["client_payload"].(map[string]any)
	if payload["branch"] != "develop" {
		t.Errorf("client_payload = %v", payload)
	}
}

func TestDispatchWithoutABranchSendsNoClientPayload(t *testing.T) {
	var got map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	if err := c.Dispatch(context.Background(), "rdcstarr/tema", DispatchSetup, ""); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, ok := got["client_payload"]; ok {
		t.Errorf("client_payload = %v, want it absent", got["client_payload"])
	}
}

func TestHooksReadsIDAndEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/rdcstarr/tema/hooks" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":     77,
			"active": true,
			"events": []string{"push", "repository_dispatch"},
			"config": map[string]any{"url": "http://1.2.3.4:9000/hook/abc"},
		}})
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	hooks, err := c.Hooks(context.Background(), "rdcstarr/tema")
	if err != nil {
		t.Fatalf("Hooks: %v", err)
	}
	if len(hooks) != 1 || hooks[0].ID != 77 || !hooks[0].Active {
		t.Fatalf("hooks = %#v", hooks)
	}
	if !hooks[0].Delivers("repository_dispatch") {
		t.Errorf("events = %v, want repository_dispatch among them", hooks[0].Events)
	}
	if hooks[0].Delivers("issues") {
		t.Error("Delivers(issues) = true")
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
