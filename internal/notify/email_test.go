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

// TestSubmissionsPortDialsTLSFirst is the regression test for the reported
// failure. A server on port 465 speaks TLS from the first byte and never sends
// an SMTP greeting, so dialing it in the clear sat waiting for one until the
// timeout expired — `smtp greeting from smtp.resend.com:465: i/o timeout`
// against credentials that were perfectly correct. The client must open the
// conversation with a TLS record instead of listening for a greeting.
//
// It drives dialSMTPConn rather than a whole VerifyEmail on port 465, which is
// privileged and would leave this test skipping on every ordinary machine.
func TestSubmissionsPortDialsTLSFirst(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	first := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		buf := make([]byte, 3)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		first <- buf
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		conn, err := dialSMTPConn(ctx, ln.Addr().String(), "127.0.0.1", true)
		if err == nil {
			_ = conn.Close()
		}
	}()

	select {
	case got := <-first:
		// 0x16 is the TLS handshake content type and 0x03 opens every supported
		// protocol version. A plaintext dial sends nothing at all here — it waits
		// for the server to greet first, which is exactly the hang being fixed.
		if got[0] != 0x16 || got[1] != 0x03 {
			t.Errorf("client opened with % x, want a TLS handshake record (16 03 ..)", got)
		}
	case <-ctx.Done():
		t.Fatal("client sent nothing — it is waiting for a greeting the submissions port never sends")
	}
}

// TestPlainPortWaitsForTheGreeting is the other half: every port but 465 must
// still start in the clear, so a STARTTLS relay on 587 or a local one on 25
// keeps working exactly as before.
func TestPlainPortWaitsForTheGreeting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	sent := make(chan int, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		buf := make([]byte, 3)
		n, _ := io.ReadFull(conn, buf)
		sent <- n
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := dialSMTPConn(ctx, ln.Addr().String(), "127.0.0.1", false)
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	select {
	case n := <-sent:
		if n != 0 {
			t.Errorf("plain dial sent %d bytes before the greeting, want none", n)
		}
	case <-ctx.Done():
		t.Fatal("server never reported what it received")
	}
}

// TestImplicitTLSAppliesOnlyToTheSubmissionsPort pins the rule that decides
// which of the two dials above a configured server gets.
func TestImplicitTLSAppliesOnlyToTheSubmissionsPort(t *testing.T) {
	for port, want := range map[string]bool{"465": true, "587": false, "25": false, "2525": false} {
		if got := implicitTLS(port); got != want {
			t.Errorf("implicitTLS(%q) = %v, want %v", port, got, want)
		}
	}
}
