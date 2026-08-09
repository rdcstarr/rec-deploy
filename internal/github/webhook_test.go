package github

import (
	"testing"
)

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	good := Sign("s3cret", body)

	if !VerifySignature("s3cret", body, good) {
		t.Error("a correctly signed body was rejected")
	}

	// A tampered body must not verify — this is the whole point of the HMAC.
	if VerifySignature("s3cret", []byte(`{"ref":"refs/heads/evil"}`), good) {
		t.Error("a tampered body verified")
	}
	if VerifySignature("wrong", body, good) {
		t.Error("the wrong secret verified")
	}
	for _, bad := range []string{"", "sha256=", "sha256=zz", "deadbeef", Sign("s3cret", body) + "0"} {
		if VerifySignature("s3cret", body, bad) {
			t.Errorf("malformed header %q verified", bad)
		}
	}
}

func TestParsePush(t *testing.T) {
	body := []byte(`{
	  "ref": "refs/heads/main",
	  "repository": {"full_name": "rdcstarr/tema"},
	  "head_commit": {"id": "abc123", "message": "fix: thing", "author": {"name": "Andrei"}}
	}`)

	ev, err := ParsePush(body)
	if err != nil {
		t.Fatalf("ParsePush: %v", err)
	}

	if ev.Ref != "refs/heads/main" || ev.Branch() != "main" {
		t.Errorf("Ref = %q, Branch = %q", ev.Ref, ev.Branch())
	}
	if ev.Repository != "rdcstarr/tema" || ev.SHA != "abc123" || ev.Message != "fix: thing" || ev.Author != "Andrei" {
		t.Errorf("ParsePush = %+v", ev)
	}
}

// A branch-delete push has no head_commit. It must not panic and must not deploy.
func TestParsePushWithoutHeadCommit(t *testing.T) {
	ev, err := ParsePush([]byte(`{"ref":"refs/heads/gone","repository":{"full_name":"rdcstarr/tema"},"head_commit":null}`))
	if err != nil {
		t.Fatalf("ParsePush: %v", err)
	}
	if ev.SHA != "" {
		t.Errorf("SHA = %q, want empty", ev.SHA)
	}
}

// A tag push is not a branch push: Branch() must report it as not a branch so
// the engine skips it instead of deploying whatever branch is checked out.
func TestParsePushOfATagIsNotABranch(t *testing.T) {
	ev, err := ParsePush([]byte(`{"ref":"refs/tags/v1.0.0","repository":{"full_name":"rdcstarr/tema"}}`))
	if err != nil {
		t.Fatalf("ParsePush: %v", err)
	}
	if ev.Branch() != "" {
		t.Errorf("Branch = %q, want empty for a tag ref", ev.Branch())
	}
}

func TestMissingScopes(t *testing.T) {
	if got := MissingScopes([]string{"repo", "admin:repo_hook", "gist"}); len(got) != 0 {
		t.Errorf("MissingScopes = %v, want none", got)
	}
	if got := MissingScopes([]string{"repo"}); len(got) != 1 || got[0] != "admin:repo_hook" {
		t.Errorf("MissingScopes = %v, want [admin:repo_hook] — named exactly", got)
	}
}
