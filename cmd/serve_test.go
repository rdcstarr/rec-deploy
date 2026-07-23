package cmd

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/deploy"
	"github.com/rdcstarr/rec-deploy/internal/store"
)

// TestDeployAndRecordRecordsThroughACancelledContext pins the half of the drain
// that lives here. server.Drain ends a deploy that outstays the shutdown budget
// by cancelling its context; if the recording rode on that same context, SQLite
// would refuse the writes and the row would stay `running` forever — the zombie
// row the daemon exists to prevent, reached through the drain meant to prevent
// it. Record on a context that survives the cancellation, or this fails.
func TestDeployAndRecordRecordsThroughACancelledContext(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	repoID, err := st.RepoInsert(context.Background(), store.Repo{
		Repository: "rdcstarr/tema", Token: "tok", Secret: "s3cret",
	})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}

	deployID, err := st.DeployStart(context.Background(), store.Deploy{
		RepoID:     repoID,
		DeliveryID: "d1",
		Ref:        "refs/heads/main",
		SHA:        "abc",
		Status:     store.StatusRunning,
	})
	if err != nil {
		t.Fatalf("DeployStart: %v", err)
	}

	// The drain's cancellation, already delivered.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// No discovery roots, so the deploy fails at once on zero installations. What
	// it fails on does not matter — that the failure still reaches the database
	// through a cancelled context does.
	deployAndRecord(ctx, st, &config.Config{}, deployID, deploy.Options{Repository: "rdcstarr/tema"})

	deploys, err := st.Deploys(context.Background(), "rdcstarr/tema", 10)
	if err != nil {
		t.Fatalf("Deploys: %v", err)
	}
	if len(deploys) != 1 {
		t.Fatalf("recorded %d deploys, want 1", len(deploys))
	}

	if got := deploys[0].Status; got != store.StatusFailed {
		t.Errorf("deploy status = %q, want %q — a cancelled deploy left its row unrecorded", got, store.StatusFailed)
	}
}

// TestReloadConfigPicksUpEdits pins the fix for the "missing email" defect: the
// daemon loaded config once at startup, so an edit made while it ran was
// invisible until the next restart. reloadConfig must re-read the file on every
// call and reflect an edit made between two calls.
func TestReloadConfigPicksUpEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	fallback := &config.Config{}

	writeNotifyConfig(t, path, "first@example.com")

	first := reloadConfig(path, fallback)
	if first.Notify.Email.To != "first@example.com" {
		t.Fatalf("Notify.Email.To = %q, want first@example.com", first.Notify.Email.To)
	}

	writeNotifyConfig(t, path, "second@example.com")

	second := reloadConfig(path, fallback)
	if second.Notify.Email.To != "second@example.com" {
		t.Errorf("Notify.Email.To = %q, want second@example.com — reload did not pick up the edit", second.Notify.Email.To)
	}
}

// TestReloadConfigFallsBackOnMalformedFile pins the fail-open half of the
// reload: a broken edit (or a file deleted out from under the daemon) must
// never take notifications down with it — reloadConfig returns the startup
// config it was given instead of panicking or returning nil.
func TestReloadConfigFallsBackOnMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("listen: [unterminated\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fallback := &config.Config{Notify: config.NotifyConfig{Email: config.EmailConfig{To: "fallback@example.com"}}}

	got := reloadConfig(path, fallback)
	if got != fallback {
		t.Errorf("reloadConfig returned %+v on a malformed file, want the fallback config unchanged", got)
	}
}

// writeNotifyConfig writes a minimal config file with notify.email.to set to
// to, so reloadConfig's caller can tell two loads of the same file apart.
func writeNotifyConfig(t *testing.T, path, to string) {
	t.Helper()

	body := "notify:\n  email:\n    to: " + to + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestServeGuardRefusesABusyAddress pins that serve refuses before it touches
// any state. ReconcileInterrupted runs a few lines later and would stamp the
// live daemon's in-flight deploys `interrupted` — and because their deliveries
// are already recorded, a redelivery is a no-op 200 and nothing would ever
// deploy them again.
func TestServeGuardRefusesABusyAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	err = serveGuard(context.Background(), ln.Addr().String())
	if err == nil {
		t.Fatal("serve accepted an address another process is already serving")
	}
	if !strings.Contains(err.Error(), "journalctl") {
		t.Errorf("the error must point at how to read the running daemon, got: %v", err)
	}
}

// TestServeGuardAcceptsAFreeAddress pins that the guard does not refuse a
// legitimate run — `serve --listen` on a free port stays possible.
func TestServeGuardAcceptsAFreeAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	if err := serveGuard(context.Background(), addr); err != nil {
		t.Fatalf("serve refused a free address: %v", err)
	}
}

// TestServeIsNotAHubEntry pins that the daemon systemd runs is not offered as
// something to pick from a menu, while staying typable and in --help.
func TestServeIsNotAHubEntry(t *testing.T) {
	cmd := newServeCmd()
	if cmd.Annotations[annotationInteractive] != "false" {
		t.Error("serve must be excluded from the interactive hub")
	}
	if cmd.Hidden {
		t.Error("serve must stay in --help and in shell completion")
	}
}
