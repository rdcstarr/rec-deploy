package github

import (
	"context"
	"fmt"
	"net/http"
)

// CreateHook registers this server's webhook — push events, HMAC secret, JSON
// body — and returns its GitHub ID. Each server registers its own hook, so
// several servers deploying one repository need no control plane.
//
// push is the only event, and the list is not a place to add to casually:
// GitHub validates the events array as a unit against what the *hook type*
// allows, and a repository webhook created with a personal access token may not
// subscribe to an app-only event. One disallowed name fails the whole call with
// a 422, which takes `repo add` down for every repository at once — as
// `repository_dispatch` did in v0.16.0. Check the event's availability list in
// GitHub's webhook documentation says `repository` before adding it here.
func (c *Client) CreateHook(ctx context.Context, repo, url, secret string) (int64, error) {
	in := map[string]any{
		"name":   "web",
		"active": true,
		"events": []string{"push"},
		"config": map[string]any{
			"url":          url,
			"content_type": "json",
			"secret":       secret,
			"insecure_ssl": "0",
		},
	}

	var out struct {
		ID int64 `json:"id"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/hooks", in, &out); err != nil {
		return 0, fmt.Errorf("github: create webhook on %s: %w", repo, err)
	}

	return out.ID, nil
}

// DeleteHook removes this server's webhook from GitHub.
func (c *Client) DeleteHook(ctx context.Context, repo string, id int64) error {
	if _, err := c.do(ctx, http.MethodDelete, "/repos/"+repo+"/hooks/"+itoa(id), nil, nil); err != nil {
		return fmt.Errorf("github: delete webhook %d from %s: %w", id, repo, err)
	}

	return nil
}
