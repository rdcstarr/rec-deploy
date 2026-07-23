package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime/multipart"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/config"
)

// emailTimeout bounds the whole SMTP exchange — dial, TLS, auth, body, quit.
// SMTP is several round trips where telegramClient's 15s covers one POST, hence
// the larger budget.
const emailTimeout = 30 * time.Second

// localName is the name announced in EHLO. It is what smtp.NewClient defaults to
// and therefore what smtp.SendMail announced.
const localName = "localhost"

// submissionsPort is the IANA "submissions" port. A server listening there
// speaks TLS from the first byte, so dialing it in the clear waits for a
// greeting that never arrives — which is how a correct Resend, Gmail or
// Fastmail configuration used to fail with an i/o timeout instead of sending.
const submissionsPort = "465"

// headerSanitizer strips CR/LF from header values before they are written
// into the raw message. Defense-in-depth: header values must never contain
// line breaks, since a break lets the value smuggle an extra header line
// (e.g. an injected Bcc). Today's inputs are trusted-ish (config, or a
// commit subject/repository name from a push), but tomorrow's callers may
// not be, and there is no reason for this class of bug to be possible.
var headerSanitizer = strings.NewReplacer("\r", " ", "\n", " ")

// VerifyEmail opens an authenticated SMTP session and closes it again without
// sending anything, so a wrong server, port, username or password is found
// while the operator is still looking at the form — not on the deploy whose
// outcome the notification was supposed to carry.
func VerifyEmail(ctx context.Context, cfg config.EmailConfig) error {
	c, closeSession, err := dialSMTP(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeSession()

	return c.Quit()
}

// implicitTLS reports whether a server on port speaks TLS from the first byte,
// so the client must open with a handshake instead of waiting for a greeting
// and upgrading with STARTTLS afterwards.
func implicitTLS(port string) bool {
	return port == submissionsPort
}

// dialSMTPConn opens the transport under ctx: a TLS handshake straight away
// when the server speaks TLS from the first byte, a plain connection the caller
// upgrades with STARTTLS otherwise. It is separate from dialSMTP so the choice
// can be exercised against a listener on any port — 465 itself is privileged,
// and a test that can only skip proves nothing.
func dialSMTPConn(ctx context.Context, address, host string, implicit bool) (net.Conn, error) {
	if implicit {
		dialer := tls.Dialer{Config: &tls.Config{ServerName: host}}

		return dialer.DialContext(ctx, "tcp", address)
	}

	var dialer net.Dialer

	return dialer.DialContext(ctx, "tcp", address)
}

// dialSMTP opens an authenticated session to cfg's server and returns it with
// the func that releases it. Port 465 is the submissions port and speaks TLS
// from the first byte; every other port starts in the clear and is upgraded
// with STARTTLS when the server advertises it.
//
// It drives the exchange by hand rather than calling smtp.SendMail, which
// neither takes a context nor sets any deadline of its own: a relay that accepts
// the connection and then goes quiet — a firewall that DROPs, a wedged relay —
// blocks the caller forever. Notification is the last thing every deploy does,
// so that would leak a goroutine and an uncounted inflight per push, and leave
// Drain waiting on work that can never finish. The steps below are SendMail's,
// with the connection bounded by a deadline.
func dialSMTP(ctx context.Context, cfg config.EmailConfig) (*smtp.Client, func(), error) {
	host, port, err := net.SplitHostPort(cfg.SMTP)
	if err != nil {
		return nil, nil, fmt.Errorf("bad smtp address %q — use `host:port`: %w", cfg.SMTP, err)
	}

	// Bounded even when the caller's context carries no deadline; an earlier one
	// still wins. The cancel outlives this function on the success path, so it
	// travels with the session and is released by the returned func.
	ctx, cancel := context.WithTimeout(ctx, emailTimeout)
	opened := false
	defer func() {
		if !opened {
			cancel()
		}
	}()

	conn, err := dialSMTPConn(ctx, cfg.SMTP, host, implicitTLS(port))
	if err != nil {
		return nil, nil, fmt.Errorf("dial smtp %s: %w", cfg.SMTP, err)
	}

	// DialContext bounds the dial; this bounds every read and write after it.
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()

		return nil, nil, fmt.Errorf("set smtp deadline: %w", err)
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()

		return nil, nil, fmt.Errorf("smtp greeting from %s: %w", cfg.SMTP, err)
	}

	// Say hello explicitly, as SendMail does. Extension() would trigger it too,
	// but it discards the error and answers false, so a relay that rejects EHLO
	// would surface below as "does not advertise AUTH" and send the operator
	// after the wrong thing.
	if err := c.Hello(localName); err != nil {
		_ = c.Close()

		return nil, nil, fmt.Errorf("smtp helo with %s: %w", cfg.SMTP, err)
	}

	// Only a session that started in the clear can be upgraded; issuing STARTTLS
	// inside the already-encrypted submissions session is a protocol error.
	if !implicitTLS(port) {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				_ = c.Close()

				return nil, nil, fmt.Errorf("starttls with %s: %w", cfg.SMTP, err)
			}
		}
	}

	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			_ = c.Close()

			return nil, nil, fmt.Errorf("smtp server %s does not advertise AUTH — clear `notify.email.username` for an unauthenticated relay", cfg.SMTP)
		}
		// PlainAuth itself refuses to hand the password to an unencrypted, non-local
		// server, so a relay that skipped STARTTLS above fails here rather than
		// leaking the credential.
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, host)); err != nil {
			_ = c.Close()

			return nil, nil, fmt.Errorf("smtp auth as %s: %w", cfg.Username, err)
		}
	}

	opened = true

	return c, func() { _ = c.Close(); cancel() }, nil
}

// sendEmail delivers the summary over SMTP. Authentication is skipped when no
// username is configured, which is what a local relay wants.
func sendEmail(ctx context.Context, cfg config.EmailConfig, subject, body, htmlBody string) error {
	msg, err := buildMessage(cfg, subject, body, htmlBody)
	if err != nil {
		return fmt.Errorf("assemble mail: %w", err)
	}

	c, closeSession, err := dialSMTP(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeSession()

	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("smtp mail from %s: %w", cfg.From, err)
	}
	if err := c.Rcpt(cfg.To); err != nil {
		return fmt.Errorf("smtp rcpt to %s: %w", cfg.To, err)
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("write mail via %s: %w", cfg.SMTP, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finish mail via %s: %w", cfg.SMTP, err)
	}

	return c.Quit()
}

// buildMessage assembles the raw RFC 5322 message. Without an HTML body it is
// the plain text/plain mail this tool always sent; with one it becomes
// multipart/alternative — plain part first, HTML last, so clients that render
// both prefer the card while everything else keeps the text.
func buildMessage(cfg config.EmailConfig, subject, body, htmlBody string) (string, error) {
	headers := []string{
		"From: " + headerSanitizer.Replace(cfg.From),
		"To: " + headerSanitizer.Replace(cfg.To),
		"Subject: " + headerSanitizer.Replace(subject),
	}

	if htmlBody == "" {
		return strings.Join(append(headers,
			"Content-Type: text/plain; charset=utf-8",
			"",
			body,
		), "\r\n"), nil
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, part := range []struct{ contentType, content string }{
		{"text/plain; charset=utf-8", body},
		{"text/html; charset=utf-8", htmlBody},
	} {
		p, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {part.contentType}})
		if err != nil {
			return "", err
		}
		if _, err := p.Write([]byte(part.content)); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	return strings.Join(append(headers,
		"MIME-Version: 1.0",
		`Content-Type: multipart/alternative; boundary="`+w.Boundary()+`"`,
		"",
		buf.String(),
	), "\r\n"), nil
}
