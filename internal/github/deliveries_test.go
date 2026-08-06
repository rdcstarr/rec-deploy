package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeliveriesDecodesWhatGitHubRecorded(t *testing.T) {
	var gotPath, gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = io.WriteString(w, `[
			{"id": 12, "guid": "aaa", "event": "push", "status": "failed to connect to host",
			 "status_code": 502, "delivered_at": "2026-08-06T10:02:02Z"},
			{"id": 11, "guid": "bbb", "event": "ping", "status": "OK",
			 "status_code": 200, "delivered_at": "2026-08-06T09:50:43Z"}
		]`)
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	got, err := c.Deliveries(context.Background(), "rdcstarr/tema", 7, 2)
	if err != nil {
		t.Fatalf("Deliveries: %v", err)
	}

	if gotPath != "/repos/rdcstarr/tema/hooks/7/deliveries" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "per_page=2" {
		t.Errorf("query = %q, want per_page=2", gotQuery)
	}

	if len(got) != 2 {
		t.Fatalf("got %d deliveries, want 2", len(got))
	}
	if got[0].ID != 12 || got[0].Event != "push" || got[0].StatusCode != 502 ||
		got[0].Status != "failed to connect to host" {
		t.Errorf("deliveries[0] = %+v", got[0])
	}
	if got[0].DeliveredAt.IsZero() {
		t.Error("deliveries[0].DeliveredAt was not decoded")
	}
	if got[1].ID != 11 || got[1].Event != "ping" || !got[1].OK() {
		t.Errorf("deliveries[1] = %+v, want a successful ping", got[1])
	}
}

func TestDeliveryOK(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		code   int
		want   bool
	}{
		{"delivered", "OK", 200, true},
		{"delivered, other 2xx", "OK", 204, true},
		{"unreachable", "failed to connect to host", 502, false},
		{"bad signature", "Bad Request", 401, false},
		{"unknown token", "Not Found", 404, false},
		// GitHub reports the outcome twice; a status that says OK over a
		// non-2xx code is not a delivery this server accepted.
		{"contradictory", "OK", 500, false},
		{"not yet answered", "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Delivery{Status: tc.status, StatusCode: tc.code}
			if got := d.OK(); got != tc.want {
				t.Errorf("OK() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPingHookPostsToThePingsEndpoint(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		// GitHub answers a ping request with 204 and no body.
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	if err := c.PingHook(context.Background(), "rdcstarr/tema", 7); err != nil {
		t.Fatalf("PingHook: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/repos/rdcstarr/tema/hooks/7/pings" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
}

func TestHookReadsTheConfiguredURLAndActive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{
			"id": 7, "active": false,
			"config": {"url": "http://1.2.3.4:9000/hook/abc", "content_type": "json"}
		}`)
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL

	got, err := c.Hook(context.Background(), "rdcstarr/tema", 7)
	if err != nil {
		t.Fatalf("Hook: %v", err)
	}
	if got.URL != "http://1.2.3.4:9000/hook/abc" {
		t.Errorf("URL = %q", got.URL)
	}
	if got.Active {
		t.Error("Active = true, want false — a deactivated hook delivers nothing")
	}
}
