package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/store"
)

const secret = "s3cret"

// harness wires a handler over a real SQLite store holding one repo, and records
// every deploy the handler dispatches.
type harness struct {
	srv *Server
	st  *store.Store

	mu         sync.Mutex
	deploys    []github.PushEvent
	dispatched chan struct{}
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	return newHarnessWith(t, nil)
}

// newHarnessWith is newHarness with a deploy body: it runs after the event has
// been recorded and the dispatch signalled, so a test can block a deploy open
// and drive Drain against it. A nil body records and returns.
func newHarnessWith(t *testing.T, deploy func(ctx context.Context)) *harness {
	t.Helper()

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := st.RepoInsert(context.Background(), store.Repo{
		Repository: "rdcstarr/tema", Token: "tok", Secret: secret,
	}); err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	h := &harness{st: st, dispatched: make(chan struct{}, 8)}
	h.srv = New(Options{
		Config: &config.Config{},
		Store:  st,
		Deploy: func(ctx context.Context, _ store.Repo, _ int64, ev github.PushEvent) {
			h.mu.Lock()
			h.deploys = append(h.deploys, ev)
			h.mu.Unlock()

			h.dispatched <- struct{}{}

			if deploy != nil {
				deploy(ctx)
			}
		},
	})

	return h
}

// post sends a signed webhook and returns the response.
func (h *harness) post(t *testing.T, token, event, delivery, body, sig string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/hook/"+token, strings.NewReader(body))
	req.Header.Set(github.EventHeader, event)
	req.Header.Set(github.DeliveryHeader, delivery)
	req.Header.Set(github.SignatureHeader, sig)

	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)

	return rec
}

// awaitDeploy waits for the handler's goroutine to dispatch one deploy. The 200
// goes out before the deploy runs, so the test cannot read the record straight
// after the response.
func (h *harness) awaitDeploy(t *testing.T) {
	t.Helper()

	select {
	case <-h.dispatched:
	case <-time.After(5 * time.Second):
		t.Fatal("no deploy dispatched within 5s")
	}
}

// dispatchedDeploys returns the deploys recorded so far.
func (h *harness) dispatchedDeploys() []github.PushEvent {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]github.PushEvent(nil), h.deploys...)
}

// signed signs body with the harness secret.
func signed(body string) string { return github.Sign(secret, []byte(body)) }

const pushBody = `{"ref":"refs/heads/main","repository":{"full_name":"rdcstarr/tema"},"head_commit":{"id":"abc","message":"m","author":{"name":"a"}}}`

func TestHealth(t *testing.T) {
	h := newHarness(t)

	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("GET /health = %d, want 200", rec.Code)
	}
}

func TestLegacyHealthzIsNotServed(t *testing.T) {
	h := newHarness(t)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /healthz = %d, want 404", rec.Code)
	}
}

func TestUnknownTokenIs404(t *testing.T) {
	h := newHarness(t)

	if rec := h.post(t, "nope", "push", "d1", pushBody, signed(pushBody)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown token = %d, want 404", rec.Code)
	}
	if got := h.dispatchedDeploys(); len(got) != 0 {
		t.Error("an unknown token dispatched a deploy")
	}
}

func TestBadSignatureIs401AndDoesNotDeploy(t *testing.T) {
	h := newHarness(t)

	rec := h.post(t, "tok", "push", "d1", pushBody, "sha256=deadbeef")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad signature = %d, want 401", rec.Code)
	}
	if got := h.dispatchedDeploys(); len(got) != 0 {
		t.Error("a forged request dispatched a deploy")
	}
}

// The signature is verified over the raw body: a tampered payload with a valid
// signature for a different payload must not pass.
func TestTamperedBodyIs401(t *testing.T) {
	h := newHarness(t)

	tampered := strings.Replace(pushBody, "refs/heads/main", "refs/heads/evil", 1)
	if rec := h.post(t, "tok", "push", "d1", tampered, signed(pushBody)); rec.Code != http.StatusUnauthorized {
		t.Errorf("tampered body = %d, want 401", rec.Code)
	}
	if got := h.dispatchedDeploys(); len(got) != 0 {
		t.Error("a tampered body dispatched a deploy")
	}
}

func TestPingIs200AndDoesNotDeploy(t *testing.T) {
	h := newHarness(t)

	body := `{"zen":"hi"}`
	if rec := h.post(t, "tok", "ping", "d1", body, signed(body)); rec.Code != http.StatusOK {
		t.Errorf("ping = %d, want 200", rec.Code)
	}
	if got := h.dispatchedDeploys(); len(got) != 0 {
		t.Error("a ping dispatched a deploy")
	}
}

func TestNonPushEventIs204(t *testing.T) {
	h := newHarness(t)

	body := `{}`
	if rec := h.post(t, "tok", "issues", "d1", body, signed(body)); rec.Code != http.StatusNoContent {
		t.Errorf("issues event = %d, want 204", rec.Code)
	}
	if got := h.dispatchedDeploys(); len(got) != 0 {
		t.Error("a non-push event dispatched a deploy")
	}
}

func TestPushDeploysOnce(t *testing.T) {
	h := newHarness(t)

	if rec := h.post(t, "tok", "push", "d1", pushBody, signed(pushBody)); rec.Code != http.StatusOK {
		t.Fatalf("push = %d, want 200", rec.Code)
	}
	h.awaitDeploy(t)

	got := h.dispatchedDeploys()
	if len(got) != 1 {
		t.Fatalf("dispatched %d deploys, want 1", len(got))
	}
	if got[0].Ref != "refs/heads/main" || got[0].SHA != "abc" {
		t.Errorf("dispatched %+v", got[0])
	}
}

// A tag push must not deploy whatever branch happens to be checked out.
func TestTagPushIs204(t *testing.T) {
	h := newHarness(t)

	body := `{"ref":"refs/tags/v1.0.0","repository":{"full_name":"rdcstarr/tema"},"head_commit":{"id":"abc"}}`
	if rec := h.post(t, "tok", "push", "d1", body, signed(body)); rec.Code != http.StatusNoContent {
		t.Errorf("tag push = %d, want 204", rec.Code)
	}
	if got := h.dispatchedDeploys(); len(got) != 0 {
		t.Error("a tag push dispatched a deploy")
	}
}

// Replaying a captured signed request must be a no-op 200. This is the hole
// an old implementation leaves open: it stores the delivery ID and never checks it.
func TestReplayedDeliveryIsANoOp(t *testing.T) {
	h := newHarness(t)

	for i := range 2 {
		if rec := h.post(t, "tok", "push", "same-delivery", pushBody, signed(pushBody)); rec.Code != http.StatusOK {
			t.Fatalf("push #%d = %d, want 200", i, rec.Code)
		}
	}
	h.awaitDeploy(t)

	if got := h.dispatchedDeploys(); len(got) != 1 {
		t.Fatalf("dispatched %d deploys for one delivery, want 1 — the replay re-deployed", len(got))
	}

	deploys, err := h.st.Deploys(context.Background(), "rdcstarr/tema", 10)
	if err != nil {
		t.Fatalf("Deploys: %v", err)
	}
	if len(deploys) != 1 {
		t.Fatalf("recorded %d deploys for one delivery, want 1", len(deploys))
	}
}

// The replay guard hangs on the delivery id, so a request without one must not
// reach it. DeployStart stores an empty id as NULL and the unique index is
// partial on NULL, so such a request would slip past dedup entirely: a captured
// signed body, replayed with the header stripped, would re-deploy on every hit.
// Posting twice is the point — both would be dispatched without the guard.
func TestPushWithoutADeliveryIDIsRejectedAndNeverDeploys(t *testing.T) {
	h := newHarness(t)

	for i := range 2 {
		if rec := h.post(t, "tok", "push", "", pushBody, signed(pushBody)); rec.Code != http.StatusBadRequest {
			t.Fatalf("push #%d without a delivery id = %d, want 400", i, rec.Code)
		}
	}

	if got := h.dispatchedDeploys(); len(got) != 0 {
		t.Errorf("a push without a delivery id dispatched %d deploys, want 0", len(got))
	}

	deploys, err := h.st.Deploys(context.Background(), "rdcstarr/tema", 10)
	if err != nil {
		t.Fatalf("Deploys: %v", err)
	}
	if len(deploys) != 0 {
		t.Fatalf("a push without a delivery id inserted %d rows, want 0 — the replay guard cannot cover them", len(deploys))
	}
}

// Only ref, sha, message and author are kept — never the raw payload.
func TestOnlyTheExtractedFieldsArePersisted(t *testing.T) {
	h := newHarness(t)

	h.post(t, "tok", "push", "d1", pushBody, signed(pushBody))
	h.awaitDeploy(t)

	deploys, err := h.st.Deploys(context.Background(), "rdcstarr/tema", 10)
	if err != nil || len(deploys) != 1 {
		t.Fatalf("Deploys = %v, %v", deploys, err)
	}

	d := deploys[0]
	if d.Ref != "refs/heads/main" || d.SHA != "abc" || d.Message != "m" || d.Author != "a" {
		t.Errorf("persisted %+v", d)
	}
	if d.DeliveryID != "d1" || d.Status != store.StatusRunning {
		t.Errorf("persisted %+v", d)
	}
	if strings.Contains(d.Message, "full_name") {
		t.Error("the raw payload leaked into the database")
	}
}

// TestDrainWaitsForADeployAlreadyRunning is the whole point of draining: the
// 200 goes out before the deploy starts, so http.Server.Shutdown — which waits
// only for requests — has never waited for the work.
func TestDrainWaitsForADeployAlreadyRunning(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})

	h := newHarnessWith(t, func(_ context.Context) {
		<-release
		close(finished)
	})

	if rec := h.post(t, "tok", "push", "d1", pushBody, signed(pushBody)); rec.Code != http.StatusOK {
		t.Fatalf("post: got %d, want 200", rec.Code)
	}
	h.awaitDeploy(t)

	drained := make(chan struct{})
	go func() {
		h.srv.Drain(context.Background())
		close(drained)
	}()

	select {
	case <-drained:
		t.Fatal("Drain returned while a deploy was still running")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not return after the deploy finished")
	}

	select {
	case <-finished:
	default:
		t.Fatal("Drain returned before the deploy finished")
	}
}

// TestDeliveryDuringADrainIsRefused covers the shutdown race. Shutdown does not
// kill a running handler: when its budget expires it returns and leaves the
// handler in place, so a delivery still blocked reading its body can reach the
// tail of hook after Drain has already observed inflight == 0 and returned. If
// hook then inserted a `running` row and dispatched a deploy nothing waits for,
// the recording would race process exit and lose — and because the dedup index
// turns any redelivery of the same GUID into a no-op 200, the row would be stuck
// `running` and the push could never deploy. So once the server is draining, hook
// must refuse the delivery with 503 before any row exists: GitHub marks it failed
// and keeps it redeliverable for the next process.
func TestDeliveryDuringADrainIsRefused(t *testing.T) {
	h := newHarness(t)

	// Drain flips the server into draining and, with nothing in flight, returns at
	// once — leaving draining set, exactly the state a late handler can still reach
	// hook in, because Shutdown abandons a slow handler when its budget expires.
	h.srv.Drain(context.Background())

	rec := h.post(t, "tok", "push", "d1", pushBody, signed(pushBody))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("delivery during a drain = %d, want 503", rec.Code)
	}

	// No row may exist: with none, GitHub marks the delivery failed and keeps it
	// redeliverable. A `running` row here would be permanent, since a redelivery of
	// the same GUID is a no-op 200.
	deploys, err := h.st.Deploys(context.Background(), "rdcstarr/tema", 10)
	if err != nil {
		t.Fatalf("Deploys: %v", err)
	}
	if len(deploys) != 0 {
		t.Fatalf("a delivery refused during a drain inserted %d rows, want 0", len(deploys))
	}
	if got := h.dispatchedDeploys(); len(got) != 0 {
		t.Errorf("a delivery refused during a drain dispatched %d deploys, want 0", len(got))
	}
}

// TestDrainCancelsADeployPastTheBudget: a deploy that outlives the drain budget
// is cancelled — but it still gets to report. A killed deploy that leaves its
// row `running` forever is the defect this project exists to remove.
func TestDrainCancelsADeployPastTheBudget(t *testing.T) {
	reported := make(chan struct{})

	h := newHarnessWith(t, func(ctx context.Context) {
		<-ctx.Done() // never returns on its own; only the drain can end it
		close(reported)
	})

	if rec := h.post(t, "tok", "push", "d1", pushBody, signed(pushBody)); rec.Code != http.StatusOK {
		t.Fatalf("post: got %d, want 200", rec.Code)
	}
	h.awaitDeploy(t)

	budget, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		h.srv.Drain(budget)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not return after cancelling the deploy")
	}

	select {
	case <-reported:
	default:
		t.Fatal("the cancelled deploy never got to report")
	}
}
