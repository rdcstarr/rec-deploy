package notify

import (
	"bufio"
	"context"
	"net"
	"strings"
	"time"
)

// localSMTPAddr is where a local mail relay listens by convention — the address
// Exim (HestiaCP) and Postfix bind on the loopback for unauthenticated local
// submission.
const localSMTPAddr = "127.0.0.1:25"

// detectTimeout bounds the local-relay probe. A loopback MTA answers in
// milliseconds, so a short budget keeps setup snappy when nothing is there.
const detectTimeout = 2 * time.Second

// DetectLocalSMTP reports localSMTPAddr when a mail server answers there with an
// SMTP greeting, and "" otherwise. It lets setup propose the local relay as the
// SMTP host: with no username configured, sendEmail skips authentication, which
// is exactly what such a relay wants. Any failure is reported as "" — detection
// is a convenience and must never block setup.
func DetectLocalSMTP(ctx context.Context) string {
	if smtpGreets(ctx, localSMTPAddr) {
		return localSMTPAddr
	}

	return ""
}

// smtpGreets reports whether addr answers a TCP connection with an SMTP 220
// greeting within detectTimeout. Confirming the 220 keeps an unrelated service
// that happens to hold the port from being mistaken for a relay.
func smtpGreets(ctx context.Context, addr string) bool {
	// One deadline for the whole probe — dial and greeting share the budget, so
	// a peer that accepts the connection then stays silent cannot stretch it to
	// twice detectTimeout.
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return false
	}

	return strings.HasPrefix(line, "220")
}
