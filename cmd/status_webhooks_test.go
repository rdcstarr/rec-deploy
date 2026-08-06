package cmd

import (
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/github"
)

func TestDeliveryFlag(t *testing.T) {
	const slug = "rdcstarr/tema"

	for _, tc := range []struct {
		name      string
		in        webhookState
		wantFlag  string // substring
		wantIssue string // substring; empty means nothing needs attention
	}{
		{
			name:     "delivering",
			in:       webhookState{Repository: slug, Known: true, Delivery: github.Delivery{ID: 1, Status: "OK", StatusCode: 200}},
			wantFlag: "delivering",
		},
		{
			// A webhook that was good and stopped being good — a firewall rebuilt,
			// an address changed, GitHub rotating its ranges. Nobody asks about it,
			// so status has to say it unprompted.
			name:      "last delivery failed",
			in:        webhookState{Repository: slug, Known: true, Delivery: github.Delivery{ID: 1, Status: "failed to connect to host", StatusCode: 502}},
			wantFlag:  "502",
			wantIssue: "rec-deploy repo check rdcstarr/tema",
		},
		{
			// Registered and never pushed to. Nothing is wrong.
			name:     "nothing delivered yet",
			in:       webhookState{Repository: slug, Known: true},
			wantFlag: "no delivery",
		},
		{
			// GitHub was not reachable from here, or no token is configured. The
			// check failed, not the webhook, and it must not be reported as one.
			name:     "unknown",
			in:       webhookState{Repository: slug},
			wantFlag: "unknown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flag, issue := deliveryFlag(tc.in)

			if !strings.Contains(flag, tc.wantFlag) {
				t.Errorf("flag = %q, want it to mention %q", flag, tc.wantFlag)
			}
			if tc.wantIssue == "" {
				if issue != "" {
					t.Errorf("issue = %q, want none", issue)
				}

				return
			}
			if !strings.Contains(issue, tc.wantIssue) {
				t.Errorf("issue = %q, want it to mention %q", issue, tc.wantIssue)
			}
			if !strings.Contains(issue, slug) {
				t.Errorf("issue = %q, want it to name the repository", issue)
			}
		})
	}
}
