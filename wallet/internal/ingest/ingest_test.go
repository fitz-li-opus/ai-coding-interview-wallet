package ingest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wallet/internal/webhookauth"
)

const (
	testSecret  = "whsec_interview_test"
	otherSecret = "whsec_other_secret"
)

func event(mods ...func(*map[string]any)) string {
	ev := map[string]any{
		"id":      "evt_8f3a1c2b9d4e5f60",
		"type":    TypeSucceeded,
		"created": 1767225600,
		"data": map[string]any{
			"payment": map[string]any{
				"id":           "pay_1a2b3c4d5e6f7a8b",
				"user_id":      "u_1",
				"amount_cents": 500,
				"credits":      500,
				"status":       "succeeded",
			},
		},
	}
	for _, m := range mods {
		m(&ev)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func payment(ev *map[string]any) map[string]any {
	return (*ev)["data"].(map[string]any)["payment"].(map[string]any)
}

func TestParseEventAcceptsAProviderEvent(t *testing.T) {
	got, err := ParseEvent([]byte(event()))
	if err != nil {
		t.Fatalf("a provider event was refused: %v", err)
	}
	if got.ID != "evt_8f3a1c2b9d4e5f60" || got.Type != TypeSucceeded ||
		got.Data.Payment.UserID != "u_1" || got.Data.Payment.Credits != 500 {
		t.Fatalf("fields did not survive parsing: %+v", got)
	}
}

func TestParseEventRefusals(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"not JSON at all", "this is not json"},
		{"truncated JSON", `{"id": "evt_1", "type":`},
		{"an array", `[]`},
		{"no event id", event(func(e *map[string]any) { delete(*e, "id") })},
		{"empty event id", event(func(e *map[string]any) { (*e)["id"] = "" })},
		{"no type", event(func(e *map[string]any) { delete(*e, "type") })},
		{"no payment id", event(func(e *map[string]any) { delete(payment(e), "id") })},
		{"no user id", event(func(e *map[string]any) { delete(payment(e), "user_id") })},
		{"fractional credits", event(func(e *map[string]any) { payment(e)["credits"] = 12.5 })},
		{"credits as a string", event(func(e *map[string]any) { payment(e)["credits"] = "500" })},
		{"zero credits on a known type", event(func(e *map[string]any) { payment(e)["credits"] = 0 })},
		{"negative credits on a known type", event(func(e *map[string]any) { payment(e)["credits"] = -500 })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseEvent([]byte(tc.raw)); err == nil {
				t.Fatal("expected a refusal, got none")
			}
		})
	}
}

// An unknown type is recorded whatever it carries: the point of recording it is
// to stop the provider retrying something nobody will act on, so it must not be
// held to the rules of the types we do act on.
func TestParseEventIsLenientWithUnknownTypes(t *testing.T) {
	raw := event(func(e *map[string]any) {
		(*e)["type"] = "payment.disputed"
		payment(e)["credits"] = 0
	})
	if _, err := ParseEvent([]byte(raw)); err != nil {
		t.Fatalf("an unknown type must still parse: %v", err)
	}
}

// A provider that adds a field tomorrow must not break a wallet written today.
func TestParseEventIgnoresUnknownFields(t *testing.T) {
	raw := event(func(e *map[string]any) {
		(*e)["livemode"] = true
		payment(e)["card4no"] = "4242"
	})
	if _, err := ParseEvent([]byte(raw)); err != nil {
		t.Fatalf("unknown fields should be ignored: %v", err)
	}
}

// Every refusal below returns before the database is touched, which is why a
// nil pool is the right pool here: if any of these ever reached Apply, the test
// would panic instead of passing quietly.
func TestHandlerRefusalsNeverReachTheDatabase(t *testing.T) {
	body := event()
	now := time.Now()
	valid := webhookauth.Sign([]byte(body), testSecret, now)

	cases := []struct {
		name   string
		header string
		body   string
		want   string
	}{
		{"nobody signed it", "", body, "SIGNATURE_INVALID"},
		{"signed with another secret", webhookauth.Sign([]byte(body), otherSecret, now), body, "SIGNATURE_INVALID"},
		{"tampered after signing", valid, strings.Replace(body, `"credits":500`, `"credits":999`, 1), "SIGNATURE_INVALID"},
		{"header we cannot parse", "t=;v1=nonsense", body, "SIGNATURE_INVALID"},
		{"correct but stale", webhookauth.Sign([]byte(body), testSecret, now.Add(-webhookauth.DefaultTolerance-time.Minute)), body, "SIGNATURE_TIMESTAMP_OUT_OF_RANGE"},
		{"signed body that is not JSON", webhookauth.Sign([]byte("not json"), testSecret, now), "not json", "INVALID_PAYLOAD"},
	}

	handler := NewHandler(nil, testSecret, webhookauth.DefaultTolerance)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhooks/payments", strings.NewReader(tc.body))
			if tc.header != "" {
				req.Header.Set(webhookauth.HeaderName, tc.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body is not JSON: %s", rec.Body.String())
			}
			if got["error"] != tc.want {
				t.Fatalf("want error %q, got %q", tc.want, got["error"])
			}
			if strings.Contains(rec.Body.String(), "whsec") ||
				(tc.header != "" && strings.Contains(rec.Body.String(), tc.header)) {
				t.Fatalf("refusal leaks the secret or the signature: %s", rec.Body.String())
			}
		})
	}
}

// The control case of signature.feature, as far as it can be taken without a
// database: a correctly signed body gets past verification and parsing.
func TestHandlerAcceptsAValidSignatureUpToApply(t *testing.T) {
	body := event()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/payments", strings.NewReader(body))
	req.Header.Set(webhookauth.HeaderName, webhookauth.Sign([]byte(body), testSecret, time.Now()))
	rec := httptest.NewRecorder()

	reached := false
	func() {
		defer func() { reached = recover() != nil }()
		NewHandler(nil, testSecret, webhookauth.DefaultTolerance).ServeHTTP(rec, req)
	}()

	if !reached {
		t.Fatalf("a correctly signed event should have reached Apply; got %d %s", rec.Code, rec.Body.String())
	}
}
