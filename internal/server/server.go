// Package server is the webhook receiver: it verifies GitHub's signature over
// the raw body, deduplicates the delivery, acknowledges immediately, and runs
// the deploy on a goroutine — GitHub's ten-second budget is never at risk.
//
// Those goroutines outlive the response, so nothing in net/http waits for them:
// on shutdown Drain does, and cancels the ones that overstay the budget, so a
// restart cannot cut a deploy in half and leave its row `running` forever.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/github"
	"github.com/rdcstarr/rec-deploy/internal/store"
)

// maxBody caps the payload GitHub can post. Its own limit is 25 MB; anything
// larger is not a webhook.
const maxBody = 25 << 20

// deployTimeout bounds the work a single delivery can start. It is generous —
// a cold `composer install` plus an `npm run build` is minutes, not seconds —
// but finite: a wedged deploy must not pin a goroutine forever.
const deployTimeout = 2 * time.Hour

// drainTimeout bounds how long a shutdown waits for the deploys already
// running. It is finite on purpose: a wedged deploy must not hold systemd in
// `stopping` for the two hours deployTimeout allows. A deploy that outlives it
// is cancelled, and reports the cancellation like any other failure.
const drainTimeout = 15 * time.Minute

// Options configures the handler.
type Options struct {
	// Config is the loaded configuration.
	Config *config.Config
	// Store is the state database.
	Store *store.Store
	// Deploy runs one deploy. It is called on a goroutine, after the 200 has
	// gone out, with a context that outlives the request.
	//
	// It must record the deploy's outcome and notify on a context that survives
	// ctx's cancellation — context.WithoutCancel. Drain cancels ctx to end a
	// deploy that overstays the shutdown budget, and an implementation that
	// records on ctx itself would find its database writes cancelled too,
	// leaving the row `running` forever: the very zombie row Drain exists to
	// prevent.
	Deploy func(ctx context.Context, repo store.Repo, deployID int64, ev github.PushEvent)
}

// Server is the webhook receiver together with the deploys it has in flight.
// The deploys are the reason it exists: they run after the response has gone
// out, so http.Server.Shutdown never waits for them and only Drain can.
type Server struct {
	opts Options
	mux  http.Handler

	// mu guards the drain state; cond signals inflight reaching zero so Drain can
	// wait for the deploys still running. hook takes mu to check draining and, if
	// it is unset, to insert the row and increment inflight in one hold — so a
	// delivery is either refused before any row exists or counted before Drain can
	// observe zero and return. inflight therefore never rises once draining is set,
	// and cond only ever counts it down to the zero Drain is waiting for.
	mu       sync.Mutex
	cond     *sync.Cond
	inflight int
	cancels  map[int64]context.CancelFunc

	// draining is set once, under mu, when Drain begins, and is never cleared: a
	// process that has begun shutting down does not resume accepting deliveries. A
	// delivery that arrives with it set is refused 503 with no row inserted, so
	// GitHub marks the delivery failed and keeps it redeliverable for the next
	// process — where a stranded `running` row could never be superseded, because
	// the dedup index turns a redelivery of the same GUID into a no-op 200.
	draining bool
}

// New builds the handler: POST /hook/{token} and GET /health.
func New(opts Options) *Server {
	s := &Server{opts: opts, cancels: make(map[int64]context.CancelFunc)}
	s.cond = sync.NewCond(&s.mu)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})

	mux.HandleFunc("POST /hook/{token}", func(w http.ResponseWriter, r *http.Request) {
		hook(w, r, s)
	})

	s.mux = mux

	return s
}

// ServeHTTP makes Server the http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Drain waits for the deploys already running. When ctx expires it cancels them
// and waits for them to finish reporting — Options.Deploy records and notifies
// on a context that survives that cancellation, so no deploy is left `running`
// in the database.
//
// Setting draining under the same lock hook uses to accept a delivery is what
// makes a delivery landing mid-shutdown safe: hook refuses it with 503 before any
// row exists rather than racing this wait, so inflight only ever falls to zero.
func (s *Server) Drain(ctx context.Context) {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()

	// sync.Cond cannot wait with a budget, so the wait moves to a goroutine and
	// the budget is selected on here.
	done := make(chan struct{})

	go func() {
		s.mu.Lock()
		for s.inflight > 0 {
			s.cond.Wait()
		}
		s.mu.Unlock()

		close(done)
	}()

	select {
	case <-done:
		return
	case <-ctx.Done():
	}

	slog.Warn("drain budget expired, cancelling the deploys still running")

	s.mu.Lock()
	for _, cancel := range s.cancels {
		cancel()
	}
	s.mu.Unlock()

	<-done
}

// hook implements the receive contract: unknown token 404, bad signature 401,
// ping 200, any other event 204, a repeated delivery 200 with no work, and a
// push acknowledged immediately and deployed on a goroutine.
func hook(w http.ResponseWriter, r *http.Request, s *Server) {
	ctx := r.Context()

	repo, err := s.opts.Store.RepoByToken(ctx, r.PathValue("token"))
	if err != nil {
		// An unknown token says nothing about which tokens exist.
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	// The HMAC runs over the raw body, before any parsing.
	if !github.VerifySignature(repo.Secret, body, r.Header.Get(github.SignatureHeader)) {
		slog.Warn("webhook signature rejected", "repository", repo.Repository)
		http.Error(w, "bad signature", http.StatusUnauthorized)

		return
	}

	switch event := r.Header.Get(github.EventHeader); event {
	case "ping":
		w.WriteHeader(http.StatusOK)
		return
	case "push":
	default:
		slog.Debug("ignoring event", "repository", repo.Repository, "event", event)
		w.WriteHeader(http.StatusNoContent)

		return
	}

	ev, err := github.ParsePush(body)
	if err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if ev.Branch() == "" || ev.SHA == "" {
		// A tag push or a branch deletion: there is nothing to deploy, and a tag
		// must never re-deploy whatever branch happens to be checked out.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	delivery := r.Header.Get(github.DeliveryHeader)
	if delivery == "" {
		// The replay guard is a unique index that is partial on NULL, and
		// DeployStart deliberately stores an empty id as NULL so that a manual
		// `rec-deploy deploy` never collides with another. A webhook that omits
		// the header would take that same path: the index would not cover it, and
		// a captured signed request could be replayed to re-deploy without limit.
		// GitHub always sends this header, so its absence is a malformed request,
		// and the NULL path stays reserved for the manual command.
		slog.Warn("webhook without a delivery id rejected", "repository", repo.Repository)
		http.Error(w, "missing delivery id", http.StatusBadRequest)

		return
	}

	// One hold of mu decides the delivery's fate. It spans the drain check, the
	// row insert and the inflight increment together, with no gap between them:
	// either Drain has already set draining and this delivery is refused before a
	// row exists, or the row is inserted and counted in inflight before Drain can
	// observe zero and return. Were these three separated, a delivery could insert
	// a `running` row after Drain had stopped waiting — a zombie row the dedup
	// index then makes permanent, since a redelivery of the same GUID is a no-op
	// 200.
	s.mu.Lock()

	if s.draining {
		s.mu.Unlock()

		// No row is inserted, so GitHub marks the delivery failed and keeps it
		// redeliverable: the next process runs it. A 200 here would strand the push
		// forever behind the dedup index.
		w.WriteHeader(http.StatusServiceUnavailable)

		return
	}

	// The dedup guard. A repeated delivery is a no-op 200: replaying a captured
	// signed request must not re-deploy. The unique index enforces it, so the
	// check stays atomic under concurrent deliveries. Holding mu across the insert
	// cannot deadlock: a deploy goroutine never holds the sole SQLite connection
	// while waiting for mu — it does its database work outside the lock and takes
	// mu only afterwards, to decrement inflight — so this insert can never wait on
	// the connection behind a goroutine that is itself waiting on mu.
	deployID, err := s.opts.Store.DeployStart(ctx, store.Deploy{
		RepoID:     repo.ID,
		DeliveryID: delivery,
		Ref:        ev.Ref,
		SHA:        ev.SHA,
		Message:    ev.Message,
		Author:     ev.Author,
		Status:     store.StatusRunning,
	})
	if errors.Is(err, store.ErrDuplicateDelivery) {
		s.mu.Unlock()
		slog.Info("duplicate delivery ignored", "repository", repo.Repository, "delivery", delivery)
		w.WriteHeader(http.StatusOK)

		return
	}
	if err != nil {
		s.mu.Unlock()
		slog.Error("cannot record deploy", "repository", repo.Repository, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// The deploy outlives the response — nothing in net/http waits for it — so it
	// runs on a context that outlives the request; only Drain can end it early.
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), deployTimeout)

	// The count and the cancel go in together, in the same hold that gated the
	// drain check: a deploy Drain can see but not cancel would hold shutdown open
	// for the whole deployTimeout, and one it can cancel but not see would be
	// abandoned mid-flight. draining is still unset here — mu has not been released
	// since the check — so the deploy is never born cancelled; only Drain's
	// expiring budget cancels it.
	s.inflight++
	s.cancels[deployID] = cancel
	s.mu.Unlock()

	// Acknowledge first, work after: GitHub gives us ten seconds and a deploy
	// takes minutes.
	w.WriteHeader(http.StatusOK)

	go func() {
		defer func() {
			s.mu.Lock()
			s.inflight--
			delete(s.cancels, deployID)
			s.cond.Broadcast()
			s.mu.Unlock()

			cancel()
		}()

		s.opts.Deploy(bg, repo, deployID, ev)
	}()
}

// ListenAndServe runs the daemon until ctx is cancelled, then shuts it down and
// drains the deploys still running before returning.
func ListenAndServe(ctx context.Context, opts Options) error {
	s := New(opts)

	srv := &http.Server{
		Addr:              opts.Config.Listen,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	slog.Info("rec-deploy listening", "addr", opts.Config.Listen)

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("listen on %s: %w", opts.Config.Listen, err)
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()

		err := srv.Shutdown(shutdown)

		drain, cancelDrain := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancelDrain()

		s.Drain(drain)

		return err
	}
}
