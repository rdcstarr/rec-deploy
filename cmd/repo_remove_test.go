package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/store"
)

// A repository deleted on GitHub takes its webhook and its deploy key with it,
// and every call against them answers 404 from then on. Reading that as a
// failure stranded the registration here: `repo remove` could not finish, so
// `repo list` went on naming a repository that existed nowhere, with no command
// able to forget it. The 404 is the outcome the command was asked for.
func TestDeleteRepoArtifactsForgetsARepositoryGitHubNoLongerHas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	client := github.New("tok")
	client.BaseURL = srv.URL

	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	id, err := st.RepoInsert(ctx, store.Repo{
		Repository:   "o/gone",
		Token:        "tok",
		Secret:       "s3cret",
		GitHubKeyID:  1,
		GitHubHookID: 2,
	})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	if err := deleteRepoArtifacts(ctx, st, client, store.Repo{
		ID: id, Repository: "o/gone", GitHubKeyID: 1, GitHubHookID: 2,
	}); err != nil {
		t.Fatalf("deleteRepoArtifacts against a deleted repository: %v", err)
	}

	// The row is the point: without it gone, the command has done nothing that
	// the operator asked for and the registration comes back on the next list.
	if _, err := st.RepoByName(ctx, "o/gone"); err == nil {
		t.Error("the registration survived a removal that reported success")
	}
}

// The other half of the same rule: a 404 is "already gone", and nothing else is.
// A token that lost its scope answers 403, and swallowing that would report a
// removal that left a live webhook delivering to this server for ever.
func TestDeleteRepoArtifactsStillFailsOnARealError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Forbidden"}`))
	}))
	defer srv.Close()

	client := github.New("tok")
	client.BaseURL = srv.URL

	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	err = deleteRepoArtifacts(ctx, st, client, store.Repo{
		ID: 1, Repository: "o/forbidden", GitHubKeyID: 1, GitHubHookID: 2,
	})
	if err == nil {
		t.Fatal("a forbidden webhook deletion reported success")
	}
	if errors.Is(err, github.ErrNotFound) {
		t.Errorf("error = %v, want something other than the not-found sentinel", err)
	}
}
