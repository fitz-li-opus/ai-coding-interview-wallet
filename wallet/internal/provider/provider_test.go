package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The body the real provider produces, with both shapes of attempt status: a
// number when the wallet answered, and a string when there was nothing there to
// answer — which is what a stopped wallet looks like from the provider's side.
const eventsBody = `{"data":[
  {"id":"evt_1","type":"payment.succeeded","created":1767225600,
   "_attempts":[{"at":1767225601,"status":"error: URLError"},{"at":1767225603,"status":200}],
   "data":{"payment":{"id":"pay_1","user_id":"u_1","amount_cents":500,"credits":500,"status":"succeeded"}}},
  {"id":"evt_2","type":"payment.refunded","created":1767225700,
   "_attempts":[],
   "data":{"payment":{"id":"pay_1","user_id":"u_1","amount_cents":500,"credits":500,"status":"refunded"}}}
]}`

func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			t.Errorf("asked for %q, want /events", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEventsParsesBothStatusShapes(t *testing.T) {
	srv := serve(t, http.StatusOK, eventsBody)

	events, err := New(srv.URL).Events(context.Background())
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	first := events[0]
	if first.ID != "evt_1" || first.Type != "payment.succeeded" || first.Created != 1767225600 {
		t.Errorf("event header wrong: %+v", first)
	}
	if got := first.Data.Payment; got.ID != "pay_1" || got.UserID != "u_1" || got.Credits != 500 || got.Status != "succeeded" {
		t.Errorf("payment wrong: %+v", got)
	}
	if len(first.Attempts) != 2 {
		t.Fatalf("got %d attempts, want 2", len(first.Attempts))
	}
	if got := first.Attempts[0].Status; got.Code != 0 || got.Text != "error: URLError" {
		t.Errorf("a failure with no answer should decode as text: %+v", got)
	}
	if got := first.Attempts[1].Status; got.Code != 200 || got.Text != "" {
		t.Errorf("an answered attempt should decode as a code: %+v", got)
	}

	// A refund carries a POSITIVE credits figure and a refunded status: the
	// direction is the event type's business, not the payload's.
	if got := events[1]; got.Type != "payment.refunded" || got.Data.Payment.Credits != 500 || got.Data.Payment.Status != "refunded" {
		t.Errorf("refund event wrong: %+v", got)
	}
	if len(events[1].Attempts) != 0 {
		t.Errorf("an event never attempted should have no attempts: %+v", events[1].Attempts)
	}
}

func TestEventsEmptyListIsNotAnError(t *testing.T) {
	srv := serve(t, http.StatusOK, `{"data":[]}`)

	events, err := New(srv.URL + "/").Events(context.Background()) // trailing slash
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("got %d events, want none", len(events))
	}
}

func TestEventsRejectsUnusableAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"a 500", http.StatusInternalServerError, `{"data":[]}`},
		{"a 404", http.StatusNotFound, `{"error":"not found"}`},
		{"not JSON at all", http.StatusOK, `<html>nope</html>`},
		{"an attempt status that is neither number nor string", http.StatusOK,
			`{"data":[{"id":"e","type":"t","created":1,"_attempts":[{"at":1,"status":{"weird":true}}],"data":{"payment":{}}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(t, tc.status, tc.body)
			_, err := New(srv.URL).Events(context.Background())
			if !errors.Is(err, ErrBadResponse) {
				t.Fatalf("got %v, want ErrBadResponse", err)
			}
		})
	}
}

func TestEventsReportsUnreachableSeparately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening there now

	_, err := New(url).Events(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("got %v, want ErrUnreachable", err)
	}
	if errors.Is(err, ErrBadResponse) {
		t.Fatal("unreachable must not also report an unusable response")
	}
}
