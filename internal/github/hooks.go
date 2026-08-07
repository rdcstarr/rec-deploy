package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

// CreateHook registers this server's webhook — push and repository_dispatch
// events, HMAC secret, JSON body — and returns its GitHub ID. Each server
// registers its own hook, so multi-server fan-out needs no control plane.
func (c *Client) CreateHook(ctx context.Context, repo, url, secret string) (int64, error) {
	in := map[string]any{
		"name":   "web",
		"active": true,
		"events": []string{"push", "repository_dispatch"},
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

// Dispatch sends a repository_dispatch, which GitHub then delivers to every
// webhook on the repository that subscribes to it. It is how an operator asks
// every server registered on a repository to run its setup pipeline, without
// touching any of them: the request carries the operator's GitHub credentials,
// and GitHub's own write-access check is the authorization.
//
// branch, when set, narrows the run to the checkouts sitting on it. It is the
// one axis that means the same thing on every server — a path does not.
func (c *Client) Dispatch(ctx context.Context, repo, eventType, branch string) error {
	in := map[string]any{"event_type": eventType}
	if branch != "" {
		in["client_payload"] = map[string]any{"branch": branch}
	}

	if _, err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/dispatches", in, nil); err != nil {
		return fmt.Errorf("github: dispatch %s on %s: %w", eventType, repo, err)
	}

	return nil
}

// HooksPerPage is the page size Hooks asks GitHub for — its maximum. GitHub's
// own default is 30, which silently cut the listing short on any repository with
// more webhooks than that. A caller that gets exactly this many back has a
// truncated list and must say so where it reports a count, rather than implying
// the number is the whole truth.
const HooksPerPage = 100

// Hooks lists every webhook on the repository — every server registered on it,
// not only this one. It answers whether a dispatch has anywhere to land.
//
// It reads one page of HooksPerPage. A repository with more webhooks than that
// is far past the fan-out this tool is built for, and following Link headers to
// prove it would buy nothing a "the list was cut short" note does not.
func (c *Client) Hooks(ctx context.Context, repo string) ([]Hook, error) {
	var out []struct {
		ID     int64    `json:"id"`
		Active bool     `json:"active"`
		Events []string `json:"events"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}

	path := "/repos/" + repo + "/hooks?per_page=" + strconv.Itoa(HooksPerPage)
	if _, err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, fmt.Errorf("github: list webhooks of %s: %w", repo, err)
	}

	hooks := make([]Hook, 0, len(out))
	for _, h := range out {
		hooks = append(hooks, Hook{ID: h.ID, URL: h.Config.URL, Active: h.Active, Events: h.Events})
	}

	return hooks, nil
}
