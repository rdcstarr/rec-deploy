package github

import (
	"context"
	"fmt"
	"net/http"
	"slices"
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
	// ID is GitHub's id for the hook. Only the listing sets it: Client.Hook is
	// asked for one by id already.
	ID int64
	// Events are the event names GitHub delivers to this hook.
	Events []string
}

// Delivers reports whether GitHub sends event to this hook. A hook registered
// before rec-deploy subscribed to repository_dispatch delivers pushes and
// nothing else, so `repo setup` would never reach the server behind it.
//
// "*" is GitHub's own wildcard for "every event, including ones added later".
// rec-deploy never registers it, but a hook created by hand or by another tool
// may carry it, and read literally such a hook — which receives everything —
// would report that it receives nothing.
func (h Hook) Delivers(event string) bool {
	return slices.Contains(h.Events, event) || slices.Contains(h.Events, "*")
}

// Hook reads one webhook's own record of itself.
func (c *Client) Hook(ctx context.Context, repo string, hookID int64) (Hook, error) {
	var out struct {
		Active bool     `json:"active"`
		Events []string `json:"events"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}

	if _, err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/hooks/"+itoa(hookID), nil, &out); err != nil {
		return Hook{}, fmt.Errorf("github: read webhook %d of %s: %w", hookID, repo, err)
	}

	return Hook{URL: out.Config.URL, Active: out.Active, Events: out.Events}, nil
}
