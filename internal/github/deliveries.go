package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Delivery is one webhook delivery GitHub attempted and recorded. It is the only
// place that answers "did this actually arrive?" — everything on this side of the
// wire can look correct while nothing GitHub sends ever lands.
type Delivery struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	Event       string    `json:"event"`
	Status      string    `json:"status"` // "OK", "failed to connect to host", …
	StatusCode  int       `json:"status_code"`
	DeliveredAt time.Time `json:"delivered_at"`
}

// OK reports whether this server accepted the delivery. GitHub records the
// outcome twice — a human-readable status and the HTTP code it got back — and
// both have to agree: a 5xx answered by a running daemon is not an acceptance.
func (d Delivery) OK() bool {
	return strings.EqualFold(d.Status, "OK") && d.StatusCode >= 200 && d.StatusCode < 300
}

// Deliveries returns the most recent deliveries GitHub recorded for a webhook,
// newest first, capped at limit.
func (c *Client) Deliveries(ctx context.Context, repo string, hookID int64, limit int) ([]Delivery, error) {
	var out []Delivery

	path := "/repos/" + repo + "/hooks/" + itoa(hookID) + "/deliveries?per_page=" + strconv.Itoa(limit)
	if _, err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, fmt.Errorf("github: list webhook deliveries of %s: %w", repo, err)
	}

	return out, nil
}

// PingHook asks GitHub to deliver a ping to a webhook. GitHub answers 204 with no
// body and delivers asynchronously; the outcome shows up under Deliveries.
func (c *Client) PingHook(ctx context.Context, repo string, hookID int64) error {
	if _, err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/hooks/"+itoa(hookID)+"/pings", nil, nil); err != nil {
		return fmt.Errorf("github: ping webhook %d on %s: %w", hookID, repo, err)
	}

	return nil
}

// Hook is a webhook as GitHub holds it: where it delivers, and whether it
// delivers at all. It is what a registration is compared against — the address
// this server would compute today may no longer be the one GitHub was told.
type Hook struct {
	URL    string
	Active bool
}

// Hook reads one webhook's own record of itself.
func (c *Client) Hook(ctx context.Context, repo string, hookID int64) (Hook, error) {
	var out struct {
		Active bool `json:"active"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}

	if _, err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/hooks/"+itoa(hookID), nil, &out); err != nil {
		return Hook{}, fmt.Errorf("github: read webhook %d of %s: %w", hookID, repo, err)
	}

	return Hook{URL: out.Config.URL, Active: out.Active}, nil
}
