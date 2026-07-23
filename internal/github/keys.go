package github

import (
	"context"
	"fmt"
	"net/http"
)

// AddDeployKey uploads a read-only deploy key and returns its GitHub ID. Read
// only: a deploy key that can push is a deploy key that can be abused.
func (c *Client) AddDeployKey(ctx context.Context, repo, title, pubkey string) (int64, error) {
	in := map[string]any{"title": title, "key": pubkey, "read_only": true}

	var out struct {
		ID int64 `json:"id"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/keys", in, &out); err != nil {
		return 0, fmt.Errorf("github: add deploy key to %s: %w", repo, err)
	}

	return out.ID, nil
}

// DeleteDeployKey removes a deploy key from GitHub. an old implementation never does
// this: `repo:delete` removes local files and leaves the key on GitHub forever.
func (c *Client) DeleteDeployKey(ctx context.Context, repo string, id int64) error {
	if _, err := c.do(ctx, http.MethodDelete, "/repos/"+repo+"/keys/"+itoa(id), nil, nil); err != nil {
		return fmt.Errorf("github: delete deploy key %d from %s: %w", id, repo, err)
	}

	return nil
}
