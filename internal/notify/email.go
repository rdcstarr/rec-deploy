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

// headerSanitizer strips CR/LF from header values before they are written
// into the raw message. Defense-in-depth: header values must never contain
// line breaks, since a break lets the value smuggle an extra header line
// (e.g. an injected Bcc). Today's inputs are trusted-ish (config, or a
// commit subject/repository name from a push), but tomorrow's callers may
// not be, and there is no reason for this class of bug to be possible.
var headerSanitizer = strings.NewReplacer("\r", " ", "\n", " ")

// sendEmail delivers the summary over SMTP. Authentication is skipped when no
// username is configured, which is what a local relay wants.
//
// It drives the exchange by hand rather than calling smtp.SendMail, which
// neither takes a context nor sets any deadline of its own: a relay that accepts
// the connection and then goes quiet — a firewall that DROPs, a wedged relay —
// blocks the caller forever. Notification is the last thing every deploy does,
// so that would leak a goroutine and an uncounted inflight per push, and leave
// Drain waiting on work that can never finish. The steps below are SendMail's,
// with the connection bounded by a deadline.
func sendEmail(ctx context.Context, cfg config.EmailConfig, subject, body, htmlBody string) error {
	host, _, err := net.SplitHostPort(cfg.SMTP)
	if err != nil {
		return fmt.Errorf("bad smtp address %q — use `host:port`: %w", cfg.SMTP, err)
	}

	msg, err := buildMessage(cfg, subject, body, htmlBody)
	if err != nil {
		return fmt.Errorf("assemble mail: %w", err)
	}

	// Bounded even when the caller's context carries no deadline; an earlier one
	// still wins.
	ctx, cancel := context.WithTimeout(ctx, emailTimeout)
	defer cancel()

	var dialer net.Dialer

	conn, err := dialer.DialContext(ctx, "tcp", cfg.SMTP)
	if err != nil {
		return fmt.Errorf("dial smtp %s: %w", cfg.SMTP, err)
	}
	defer func() { _ = conn.Close() }()

	// DialContext bounds the dial; this bounds every read and write after it.
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set smtp deadline: %w", err)
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp greeting from %s: %w", cfg.SMTP, err)
	}
	defer func() { _ = c.Close() }()

	// Say hello explicitly, as SendMail does. Extension() would trigger it too,
	// but it discards the error and answers false, so a relay that rejects EHLO
	// would surface below as "does not advertise AUTH" and send the operator
	// after the wrong thing.
	if err := c.Hello(localName); err != nil {
		return fmt.Errorf("smtp helo with %s: %w", cfg.SMTP, err)
	}

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("starttls with %s: %w", cfg.SMTP, err)
		}
	}

	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return fmt.Errorf("smtp server %s does not advertise AUTH — clear `notify.email.username` for an unauthenticated relay", cfg.SMTP)
		}
		// PlainAuth itself refuses to hand the password to an unencrypted, non-local
		// server, so a relay that skipped STARTTLS above fails here rather than
		// leaking the credential.
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, host)); err != nil {
			return fmt.Errorf("smtp auth as %s: %w", cfg.Username, err)
		}
	}

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
