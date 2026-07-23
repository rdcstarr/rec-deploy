package github

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// NewToken mints the unguessable path segment of this server's webhook URL.
// Knowing it is not enough to forge a delivery — the HMAC still has to check out
// — but an unknown token is a 404, which keeps the surface small.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate webhook token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewSecret mints the HMAC-SHA256 secret GitHub signs deliveries with. It is
// never logged and never printed.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}

	return hex.EncodeToString(b), nil
}

// HookURL is the webhook URL registered on GitHub for this server.
func HookURL(publicURL, token string) (string, error) {
	if strings.TrimSpace(publicURL) == "" {
		return "", fmt.Errorf("public_url is not configured — run `rec-deploy init`, or `rec-deploy config set public_url http://<ip>:9000`")
	}

	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("public_url %q must be an absolute URL such as http://1.2.3.4:9000", publicURL)
	}

	return strings.TrimRight(u.String(), "/") + "/hook/" + token, nil
}

// slugPattern matches an owner/repo pair over the character set GitHub allows in
// both halves — and nothing else, since the slug is spliced straight into an API
// path (/repos/{slug}/keys) and into the SSH clone URL.
var slugPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ValidateSlug checks that slug is a plain owner/repo pair, so operator input
// can never climb out of the API path it is interpolated into.
func ValidateSlug(slug string) error {
	owner, repo, _ := strings.Cut(slug, "/")
	if !slugPattern.MatchString(slug) || isDots(owner) || isDots(repo) {
		return fmt.Errorf("%q is not a repository — pass it as owner/repo, e.g. rdcstarr/tema-mea", slug)
	}

	return nil
}

// isDots reports whether s is made of dots alone ("." or ".."), which the
// character class allows but a URL path must never carry.
func isDots(s string) bool { return strings.Trim(s, ".") == "" }

// UpdateHook rewrites this server's webhook — the whole config, not just the
// rotated secret: GitHub replaces the config object wholesale, so a partial body
// would blank the delivery URL and re-open insecure_ssl.
func (c *Client) UpdateHook(ctx context.Context, repo string, id int64, url, secret string) error {
	in := map[string]any{
		"active": true,
		"events": []string{"push"},
		"config": map[string]any{
			"url":          url,
			"content_type": "json",
			"secret":       secret,
			"insecure_ssl": "0",
		},
	}

	if _, err := c.do(ctx, http.MethodPatch, "/repos/"+repo+"/hooks/"+itoa(id), in, nil); err != nil {
		return fmt.Errorf("github: update webhook %d on %s: %w", id, repo, err)
	}

	return nil
}
