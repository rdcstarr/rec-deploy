package notify

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/config"
)

var msgCfg = config.EmailConfig{
	SMTP: "smtp.example.com:587",
	From: "deploy@example.com",
	To:   "ops@example.com",
}

// TestBuildMessagePlainWhenNoHTML pins the fallback shape: without an HTML
// body the message is exactly the historical single-part text/plain mail.
func TestBuildMessagePlainWhenNoHTML(t *testing.T) {
	msg, err := buildMessage(msgCfg, "subj", "body\n", "")
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}

	m, err := mail.ReadMessage(strings.NewReader(msg))
	if err != nil {
		t.Fatalf("message does not parse: %v", err)
	}
	if ct := m.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want plain utf-8", ct)
	}
	got, _ := io.ReadAll(m.Body)
	if string(got) != "body\n" {
		t.Errorf("body = %q", got)
	}
}

// TestBuildMessageMultipartAlternative pins the two-part shape: plain first,
// HTML last (clients prefer the last alternative they support).
func TestBuildMessageMultipartAlternative(t *testing.T) {
	msg, err := buildMessage(msgCfg, "subj", "plain body", "<html>card</html>")
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}

	m, err := mail.ReadMessage(strings.NewReader(msg))
	if err != nil {
		t.Fatalf("message does not parse: %v", err)
	}
	if m.Header.Get("MIME-Version") != "1.0" {
		t.Errorf("missing MIME-Version header")
	}
	mediaType, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q (%v), want multipart/alternative", mediaType, err)
	}

	r := multipart.NewReader(m.Body, params["boundary"])
	types := []string{}
	bodies := []string{}
	for {
		p, err := r.NextPart()
		if err != nil {
			break
		}
		types = append(types, p.Header.Get("Content-Type"))
		b, _ := io.ReadAll(p)
		bodies = append(bodies, string(b))
	}
	if len(types) != 2 || types[0] != "text/plain; charset=utf-8" || types[1] != "text/html; charset=utf-8" {
		t.Fatalf("parts = %v, want plain then html", types)
	}
	if bodies[0] != "plain body" || !strings.Contains(bodies[1], "card") {
		t.Errorf("part bodies wrong: %q / %q", bodies[0], bodies[1])
	}
}

// TestBuildMessageFlattensHeaderInjection pins the header-injection defense:
// a CR/LF embedded in a header value must never smuggle an extra header line
// (e.g. a Bcc) into the message.
func TestBuildMessageFlattensHeaderInjection(t *testing.T) {
	msg, err := buildMessage(msgCfg, "x\r\nBcc: evil@example.com", "body", "")
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}

	m, err := mail.ReadMessage(strings.NewReader(msg))
	if err != nil {
		t.Fatalf("message does not parse: %v", err)
	}
	if m.Header.Get("Bcc") != "" {
		t.Errorf("injected Bcc header survived: %q", m.Header.Get("Bcc"))
	}
	if got := m.Header.Get("Subject"); !strings.Contains(got, "Bcc: evil@example.com") {
		t.Errorf("Subject = %q, want the injected text flattened into it", got)
	}
}

// A relay that completes the TCP handshake and then says nothing must not hang
// the caller. smtp.SendMail sets no deadline of its own, so this blocked
// forever: notification is the last thing every deploy does, so each push
// leaked a goroutine and an uncounted inflight, and Drain waited on work that
// could never finish. The context's earlier deadline must win over emailTimeout.
func TestSendEmailGivesUpOnASilentRelay(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	stop := make(chan struct{})
	defer close(stop)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		<-stop // hold it open, never send the SMTP greeting
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	cfg := config.EmailConfig{SMTP: ln.Addr().String(), From: "from@example.com", To: "to@example.com"}

	done := make(chan error, 1)
	go func() { done <- sendEmail(ctx, cfg, "subject", "body", "") }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a silent relay returned no error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sendEmail never gave up on a silent relay — it would block a deploy's goroutine forever")
	}
}
