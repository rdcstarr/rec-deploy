package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RequiredScopes are the token scopes rec-deploy needs: repo to read the repository
// and manage its deploy keys, admin:repo_hook to manage its webhooks.
var RequiredScopes = []string{"repo", "admin:repo_hook"}

// Token resolves a GitHub token: the rec-deploy config first, then GITHUB_TOKEN /
// GH_TOKEN, then the gh CLI. gh is supported but never required — a bare
// HestiaCP box does not have it.
func Token(ctx context.Context, configured string) (string, error) {
	if v := strings.TrimSpace(configured); v != "" {
		return v, nil
	}

	for _, env := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, nil
		}
	}

	if _, err := exec.LookPath("gh"); err == nil {
		if out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				return v, nil
			}
		}
	}

	return "", fmt.Errorf("github token is not configured — run `rec-deploy init`, or set GITHUB_TOKEN")
}

// MissingScopes returns the RequiredScopes absent from have, so the setup wizard
// can name exactly what is missing instead of failing vaguely.
func MissingScopes(have []string) []string {
	set := make(map[string]bool, len(have))
	for _, s := range have {
		set[strings.TrimSpace(s)] = true
	}

	var missing []string
	for _, want := range RequiredScopes {
		if !set[want] {
			missing = append(missing, want)
		}
	}

	return missing
}
