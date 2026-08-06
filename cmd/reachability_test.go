package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/github"
)

func TestWebhookEndpoint(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://1.2.3.4:9000", "1.2.3.4:9000"},
		{"http://1.2.3.4:9000/", "1.2.3.4:9000"},
		// A URL with no port still names one: an operator opening a firewall needs
		// the number GitHub connects to, not a hostname.
		{"https://deploy.example.com", "deploy.example.com:443"},
		{"http://deploy.example.com", "deploy.example.com:80"},
		{"", ""},
		{"not a url", ""},
	} {
		if got := webhookEndpoint(tc.in); got != tc.want {
			t.Errorf("webhookEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDiagnoseWebhook(t *testing.T) {
	const slug, endpoint = "rdcstarr/tema", "1.2.3.4:9000"

	failed := func(status string, code int) github.Reachability {
		return github.Reachability{
			State:    github.Failed,
			Delivery: github.Delivery{Event: "ping", Status: status, StatusCode: code},
		}
	}

	for _, tc := range []struct {
		name       string
		in         github.Reachability
		wantCause  string   // substring
		wantFix    []string // substrings, all required
		wantRanges bool
	}{
		{
			// GitHub synthesises 502 when it cannot get an answer at all. The
			// daemon being down looks the same from there as a dropped packet, so
			// the fix names both rather than guessing.
			name:       "cannot connect",
			in:         failed("failed to connect to host", 502),
			wantCause:  endpoint,
			wantFix:    []string{"rec-deploy status"},
			wantRanges: true,
		},
		{
			name:       "timed out",
			in:         failed("timed out", 0),
			wantCause:  endpoint,
			wantRanges: true,
		},
		{
			// The packet arrived and was rejected, so the network is not the
			// problem and the delivery ranges would be noise.
			name:       "bad signature",
			in:         failed("Unauthorized", 401),
			wantCause:  "secret",
			wantFix:    []string{"rec-deploy repo rotate rdcstarr/tema"},
			wantRanges: false,
		},
		{
			name:       "unknown token",
			in:         failed("Not Found", 404),
			wantCause:  "url",
			wantFix:    []string{"public_url"},
			wantRanges: false,
		},
		{
			name:       "this server errored",
			in:         failed("Internal Server Error", 500),
			wantCause:  "500",
			wantFix:    []string{"rec-deploy logs"},
			wantRanges: false,
		},
		{
			name:       "the hook is gone",
			in:         github.Reachability{State: github.Failed, Detail: "the webhook no longer exists on github"},
			wantCause:  "no longer exists",
			wantFix:    []string{"rec-deploy repo add rdcstarr/tema"},
			wantRanges: false,
		},
		{
			// Nothing is known yet, so nothing is asserted about the firewall.
			name:       "pending",
			in:         github.Reachability{State: github.Pending},
			wantCause:  "nothing has been recorded",
			wantFix:    []string{"rec-deploy repo check rdcstarr/tema"},
			wantRanges: false,
		},
		{
			name:      "unknown",
			in:        github.Reachability{Detail: "Resource not accessible (HTTP 403)"},
			wantCause: "Resource not accessible (HTTP 403)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := diagnoseWebhook(slug, endpoint, tc.in)

			if !strings.Contains(strings.ToLower(got.Cause), strings.ToLower(tc.wantCause)) {
				t.Errorf("Cause = %q, want it to mention %q", got.Cause, tc.wantCause)
			}
			fix := strings.Join(got.Fix, "\n")
			for _, want := range tc.wantFix {
				if !strings.Contains(fix, want) {
					t.Errorf("Fix = %q, want it to mention %q", fix, want)
				}
			}
			if got.Ranges != tc.wantRanges {
				t.Errorf("Ranges = %v, want %v", got.Ranges, tc.wantRanges)
			}
		})
	}
}

// The report an operator actually reads. "webhook unreachable" sends them
// hunting; this pins that the failure names the port GitHub could not get an
// answer from and the ranges to allow, both on screen at the moment it fails.
func TestRenderReachabilityPrintsThePortAndTheRanges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ssh_keys":["ssh-ed25519 AAAA"],"hooks":["192.30.252.0/22","2a0a:a440::/29"]}`)
	}))
	defer srv.Close()

	previous := github.MetaURL
	github.MetaURL = srv.URL
	t.Cleanup(func() { github.MetaURL = previous })

	out := capture(t, func() {
		renderReachability(context.Background(), "rdcstarr/tema", "http://1.2.3.4:9000", github.Reachability{
			State: github.Failed,
			Delivery: github.Delivery{
				ID: 12, Event: "ping", Status: "failed to connect to host", StatusCode: 502,
			},
		})
	})

	for _, want := range []string{
		"1.2.3.4:9000",
		"failed to connect to host",
		"rec-deploy status",
		"192.30.252.0/22",
		"2a0a:a440::/29",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the failure report does not mention %q:\n%s", want, out)
		}
	}
}

// A check that could not run says so and stops. Printing a firewall fix under a
// 403 would send the operator to open a port that was never the problem.
func TestRenderReachabilitySaysNothingActionableWhenItCouldNotAsk(t *testing.T) {
	out := capture(t, func() {
		renderReachability(context.Background(), "rdcstarr/tema", "http://1.2.3.4:9000", github.Reachability{
			Detail: "Resource not accessible by personal access token (HTTP 403)",
		})
	})

	if !strings.Contains(out, "Resource not accessible") {
		t.Errorf("the report does not say why it could not check:\n%s", out)
	}
	if strings.Contains(out, "192.30") || strings.Contains(out, "allow") {
		t.Errorf("the report offers a firewall fix for a question it never asked:\n%s", out)
	}
}

// A working webhook has nothing to diagnose, and printing a fix under a success
// would send the operator hunting for a problem they do not have.
func TestDiagnoseWebhookSaysNothingWhenItIsReachable(t *testing.T) {
	got := diagnoseWebhook("rdcstarr/tema", "1.2.3.4:9000", github.Reachability{
		State:    github.Reachable,
		Delivery: github.Delivery{Event: "ping", Status: "OK", StatusCode: 200},
	})

	if got.Cause != "" || len(got.Fix) != 0 || got.Ranges {
		t.Errorf("diagnoseWebhook on a reachable hook = %+v, want the zero value", got)
	}
}
