package notify

import (
	"context"
	"net"
	"testing"
)

// greeter starts a loopback listener that writes greeting to the first client
// and closes, returning its address. It models an MTA's opening line.
func greeter(t *testing.T, greeting string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte(greeting))
	}()

	return ln.Addr().String()
}

// TestSMTPGreetsOn220 pins that a 220 greeting is recognised as SMTP.
func TestSMTPGreetsOn220(t *testing.T) {
	addr := greeter(t, "220 mail.example.com ESMTP\r\n")
	if !smtpGreets(context.Background(), addr) {
		t.Error("a 220 greeting must be detected as SMTP")
	}
}

// TestSMTPGreetsRejectsNon220 pins that an open port answering with something
// other than 220 is not mistaken for a relay.
func TestSMTPGreetsRejectsNon220(t *testing.T) {
	addr := greeter(t, "500 go away\r\n")
	if smtpGreets(context.Background(), addr) {
		t.Error("a non-220 greeting must not be taken for SMTP")
	}
}

// TestSMTPGreetsClosedPort pins that a port with nothing listening is not SMTP.
func TestSMTPGreetsClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // free the port; nothing answers there now

	if smtpGreets(context.Background(), addr) {
		t.Error("a closed port must not be detected as SMTP")
	}
}
