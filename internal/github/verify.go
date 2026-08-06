package github

import (
	"context"
	"errors"
	"time"
)

// Reach is what a reachability check concluded.
type Reach int

const (
	// Unknown means the check itself could not run — no token scope, no network.
	// It is the zero value on purpose: a check that never ran must never read as
	// a verdict.
	Unknown Reach = iota
	// Reachable means GitHub delivered a ping and this server accepted it.
	Reachable
	// Failed means GitHub recorded a delivery that did not succeed.
	Failed
	// Pending means GitHub had recorded nothing either way before the budget ran
	// out. It is not a failure: nothing is known yet.
	Pending
)

// String renders the state for a --json field and for logs.
func (r Reach) String() string {
	switch r {
	case Reachable:
		return "reachable"
	case Failed:
		return "failed"
	case Pending:
		return "pending"
	default:
		return "unknown"
	}
}

// Reachability is the verdict on whether GitHub can deliver to this server.
type Reachability struct {
	State    Reach
	Delivery Delivery // the delivery the verdict rests on; zero unless one was read
	Detail   string   // why the check could not answer, or what else is wrong
}

// pollInterval is how often VerifyHook re-reads the delivery list. It is a var so
// tests do not have to wait in real time.
var pollInterval = 750 * time.Millisecond

// deliveryPage is how many deliveries a poll reads. A ping lands at the head of
// the list, and a handful of rows is enough to still find it if a push or two
// races it.
const deliveryPage = 10

// VerifyHook answers whether GitHub can actually deliver to this server, by
// triggering a ping and waiting for GitHub's own record of what happened to it.
//
// The daemon answers a ping with 200 only after the HMAC check, so a delivered
// ping proves the whole chain at once: the public address, the firewall, the
// /hook/{token} path segment and the shared secret. A wrong secret comes back
// 401, an unknown token 404, a dropped packet 502.
//
// It triggers its own ping rather than reading the one GitHub sends when a hook
// is created, because that one races the store row `repo add` writes afterwards:
// a ping that arrives first finds no repository and is answered 404, which would
// report a healthy server as broken.
//
// The fresh ping is identified by an ID absent from the list read before it was
// requested. Comparing IDs rather than timestamps keeps this correct regardless
// of clock skew against GitHub, and tells our ping apart from an older one that
// may well have succeeded.
//
// It returns no error: failing to *ask* is Unknown, never a verdict. The caller
// bounds the wait with ctx.
func (c *Client) VerifyHook(ctx context.Context, repo string, hookID int64) Reachability {
	before, err := c.Deliveries(ctx, repo, hookID, deliveryPage)
	if err != nil {
		return unreadable(err)
	}

	seen := make(map[int64]bool, len(before))
	for _, d := range before {
		seen[d.ID] = true
	}

	if err := c.PingHook(ctx, repo, hookID); err != nil {
		return unreadable(err)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return Reachability{Detail: "the check was cancelled"}
			}

			return Reachability{State: Pending}
		case <-ticker.C:
		}

		now, err := c.Deliveries(ctx, repo, hookID, deliveryPage)
		if err != nil {
			// The list was readable a moment ago, so this is a blip rather than a
			// verdict. Keep polling until the budget decides.
			continue
		}

		for _, d := range now {
			if d.Event != "ping" || seen[d.ID] {
				continue
			}
			if d.OK() {
				return Reachability{State: Reachable, Delivery: d}
			}

			return Reachability{State: Failed, Delivery: d}
		}
	}
}

// unreadable turns a failed API call into a verdict: a hook GitHub no longer has
// is a broken registration, while anything else — a token without the scope, a
// network this box cannot use — leaves the question open.
func unreadable(err error) Reachability {
	if errors.Is(err, ErrNotFound) {
		return Reachability{State: Failed, Detail: "the webhook no longer exists on github"}
	}

	return Reachability{Detail: err.Error()}
}
