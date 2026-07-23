// Package github administers the GitHub side of a deployment: token
// resolution, deploy keys, webhooks, and verification of the webhook
// signatures GitHub sends back. GitHub only — no forge abstraction.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rdcstarr/rec-deploy/internal/retry"
)

// APIBaseURL is GitHub's REST API root.
const APIBaseURL = "https://api.github.com"

// ErrNotFound is wrapped into the error returned by any API call that gets a
// 404 — callers use errors.Is to tell "already gone" apart from a real
// failure, e.g. a delete racing a resource someone removed by hand.
var ErrNotFound = errors.New("not found on github")

// Client is an authenticated GitHub REST client.
type Client struct {
	// BaseURL is the API root. It is a field so tests can point the client at
	// an httptest.Server.
	BaseURL string

	token string
	http  *http.Client
}

// New returns a Client that authenticates every request with token.
func New(token string) *Client {
	return &Client{
		BaseURL: APIBaseURL,
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// User is the account behind a token, together with the scopes GitHub grants
// it. Scopes come from the X-OAuth-Scopes response header — the only place
// GitHub reports what a token is actually allowed to do.
type User struct {
	Login  string
	Scopes []string
}

// User fetches the authenticated user and the token's scopes, so `rec-deploy init`
// can validate a token and name exactly which scopes are missing.
func (c *Client) User(ctx context.Context) (User, error) {
	var out struct {
		Login string `json:"login"`
	}

	header, err := c.do(ctx, http.MethodGet, "/user", nil, &out)
	if err != nil {
		return User{}, fmt.Errorf("github: get user: %w", err)
	}

	u := User{Login: out.Login}
	for _, scope := range strings.Split(header.Get("X-OAuth-Scopes"), ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			u.Scopes = append(u.Scopes, scope)
		}
	}

	return u, nil
}

// do performs one authenticated API call and returns the response headers,
// decoding a 2xx body into out when out is non-nil. Network errors, 429 and 5xx
// are retried with backoff — a transient GitHub failure must not lose a deploy
// key — while a 4xx is permanent: retrying a rejected token only burns quota.
func (c *Client) do(ctx context.Context, method, path string, in, out any) (http.Header, error) {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		body = b
	}

	var header http.Header

	err := retry.Do(ctx, retry.Default, func() error {
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
		if err != nil {
			return retry.Permanent(err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return err // transient network error
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return errors.New(apiError(resp)) // transient — retry
		}
		if resp.StatusCode == http.StatusNotFound {
			return retry.Permanent(fmt.Errorf("%s: %w", apiError(resp), ErrNotFound))
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return retry.Permanent(errors.New(apiError(resp))) // 4xx — fatal
		}

		header = resp.Header
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return retry.Permanent(fmt.Errorf("decode response: %w", err))
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return header, nil
}

// apiError renders a failed response as GitHub's own error message plus the
// status, so the operator sees "Bad credentials (HTTP 401)" and not "HTTP 401".
func apiError(resp *http.Response) string {
	var e struct {
		Message string `json:"message"`
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Message, resp.StatusCode)
	}

	return fmt.Sprintf("HTTP %d", resp.StatusCode)
}

// itoa renders a GitHub object ID for a URL path.
func itoa(id int64) string { return strconv.FormatInt(id, 10) }
