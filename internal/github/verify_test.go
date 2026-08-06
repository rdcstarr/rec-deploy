package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// pollFast shrinks the poll interval so a test that waits for several rounds
// runs in milliseconds instead of seconds.
func pollFast(t *testing.T) {
	t.Helper()

	previous := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = previous })
}

// deliveryStub serves /deliveries from a script — one entry per GET, the last
// entry repeating — and records whether a ping was requested.
type deliveryStub struct {
	pages  []string
	gets   int
	pinged bool
	status int // non-zero: answer every request with it
}

func (s *deliveryStub) serve(t *testing.T) *Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.status != 0 {
			w.WriteHeader(s.status)
			_, _ = io.WriteString(w, `{"message":"Resource not accessible by personal access token"}`)

			return
		}
		if r.Method == http.MethodPost {
			s.pinged = true
			w.WriteHeader(http.StatusNoContent)

			return
		}

		page := s.pages[min(s.gets, len(s.pages)-1)]
		s.gets++
		_, _ = io.WriteString(w, page)
	}))
	t.Cleanup(srv.Close)

	c := New("tok")
	c.BaseURL = srv.URL

	return c
}

func verifyCtx(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	return ctx
}

func TestVerifyHookReportsReachableWhenTheFreshPingArrives(t *testing.T) {
	pollFast(t)

	// Delivery 11 is an older ping that also succeeded. Keying on "a ping that is
	// OK" would report the verdict of a delivery from before this check ran, so
	// only an ID absent from the first read counts.
	const old = `{"id":11,"event":"ping","status":"OK","status_code":200,"delivered_at":"2026-08-06T09:50:43Z"}`
	const fresh = `{"id":12,"event":"ping","status":"OK","status_code":200,"delivered_at":"2026-08-06T10:02:02Z"}`

	stub := &deliveryStub{pages: []string{
		`[` + old + `]`, // the snapshot taken before the ping
		`[` + old + `]`, // GitHub has not recorded it yet
		`[` + old + `]`,
		`[` + fresh + `,` + old + `]`,
	}}
	c := stub.serve(t)

	got := c.VerifyHook(verifyCtx(t), "rdcstarr/tema", 7)

	if got.State != Reachable {
		t.Fatalf("State = %v (%s), want Reachable", got.State, got.Detail)
	}
	if got.Delivery.ID != 12 {
		t.Errorf("Delivery.ID = %d, want the fresh ping 12", got.Delivery.ID)
	}
	if !stub.pinged {
		t.Error("VerifyHook did not request a ping")
	}
}

func TestVerifyHookReportsFailedWhenGitHubCannotReachTheServer(t *testing.T) {
	pollFast(t)

	stub := &deliveryStub{pages: []string{
		`[]`,
		`[{"id":12,"event":"ping","status":"failed to connect to host","status_code":502,
		   "delivered_at":"2026-08-06T10:02:02Z"}]`,
	}}
	c := stub.serve(t)

	got := c.VerifyHook(verifyCtx(t), "rdcstarr/tema", 7)

	if got.State != Failed {
		t.Fatalf("State = %v, want Failed", got.State)
	}
	if got.Delivery.StatusCode != 502 || got.Delivery.Status != "failed to connect to host" {
		t.Errorf("Delivery = %+v, want the recorded failure", got.Delivery)
	}
}

// A push landing between the snapshot and the ping is a new delivery too, and
// answering with its verdict would report the wrong event's outcome.
func TestVerifyHookIgnoresNewDeliveriesThatAreNotItsPing(t *testing.T) {
	pollFast(t)

	stub := &deliveryStub{pages: []string{
		`[]`,
		`[{"id":99,"event":"push","status":"OK","status_code":200,"delivered_at":"2026-08-06T10:00:00Z"}]`,
		`[{"id":100,"event":"ping","status":"OK","status_code":200,"delivered_at":"2026-08-06T10:02:02Z"},
		  {"id":99,"event":"push","status":"OK","status_code":200,"delivered_at":"2026-08-06T10:00:00Z"}]`,
	}}
	c := stub.serve(t)

	got := c.VerifyHook(verifyCtx(t), "rdcstarr/tema", 7)

	if got.State != Reachable || got.Delivery.ID != 100 {
		t.Fatalf("got %v on delivery %d, want Reachable on the ping 100", got.State, got.Delivery.ID)
	}
}

// Waiting longer than the budget is not a failure of the webhook: nothing has
// been recorded either way, and saying "unreachable" would be a guess.
func TestVerifyHookReportsPendingWhenNothingIsRecordedInTime(t *testing.T) {
	pollFast(t)

	stub := &deliveryStub{pages: []string{`[]`}}
	c := stub.serve(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	got := c.VerifyHook(ctx, "rdcstarr/tema", 7)

	if got.State != Pending {
		t.Fatalf("State = %v, want Pending", got.State)
	}
}

// A token that cannot read deliveries makes the check unanswerable. That must
// never be reported as a failure of the thing being checked, and an otherwise
// correct registration must not be turned into an error by it.
func TestVerifyHookReportsUnknownWhenItCannotRead(t *testing.T) {
	stub := &deliveryStub{status: http.StatusForbidden}
	c := stub.serve(t)

	got := c.VerifyHook(verifyCtx(t), "rdcstarr/tema", 7)

	if got.State != Unknown {
		t.Fatalf("State = %v, want Unknown", got.State)
	}
	if got.Detail == "" {
		t.Error("Unknown carries no Detail — the operator is told nothing")
	}
	if stub.pinged {
		t.Error("VerifyHook pinged despite being unable to read the outcome")
	}
}

// A hook deleted on GitHub by hand is a broken registration, not an unanswerable
// question: this server will never be delivered to again.
func TestVerifyHookReportsFailedWhenTheHookIsGone(t *testing.T) {
	stub := &deliveryStub{status: http.StatusNotFound}
	c := stub.serve(t)

	got := c.VerifyHook(verifyCtx(t), "rdcstarr/tema", 7)

	if got.State != Failed {
		t.Fatalf("State = %v, want Failed", got.State)
	}
	if got.Detail == "" {
		t.Error("Failed carries no Detail for a hook that no longer exists")
	}
}
