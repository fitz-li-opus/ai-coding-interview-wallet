package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestHandler() http.Handler {
	return New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestHealthIsCheapAndSaysOk(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status field = %q, want %q", body.Status, "ok")
	}
	if strings.Contains(rec.Body.String(), "whsec") {
		t.Fatal("health leaked something that looks like the signing secret")
	}
}

func TestClientsRequestIDComesBack(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-Id", "req-abc-123")

	rec := httptest.NewRecorder()
	newTestHandler().ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-Id"); got != "req-abc-123" {
		t.Fatalf("X-Request-Id = %q, want the client's own id back", got)
	}
}

func TestAResponseCarriesARequestIDEvenWhenTheClientSentNone(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	got := rec.Header().Get("X-Request-Id")
	if got == "" {
		t.Fatal("no X-Request-Id: a response that cannot be found in a log")
	}
	if !strings.Contains(got, "-") {
		t.Fatalf("generated request id %q is not UUID-shaped", got)
	}
}

func TestRequestIDStripsControlCharacters(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-Id", "req-\x00abc")

	rec := httptest.NewRecorder()
	newTestHandler().ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-Id"); got != "req-abc" {
		t.Fatalf("X-Request-Id = %q, want control characters removed", got)
	}
}

// The two response shapes the contract pins down with additionalProperties:
// false. A third key here would be a contract change.
func TestWalletResponseShape(t *testing.T) {
	b := mustMarshal(walletResponse{UserID: "u_1", Balance: -40})
	if string(b) != `{"user_id":"u_1","balance":-40}` {
		t.Fatalf("wallet body = %s", b)
	}
}

func TestLedgerResponseShape(t *testing.T) {
	payment := "pay_1"
	event := "evt_1"
	b := mustMarshal(ledgerResponse{
		UserID:  "u_1",
		Balance: 400,
		Entries: []entryResponse{
			{Kind: "credit", Amount: 500, PaymentID: &payment, EventID: &event},
			{Kind: "spend", Amount: -100},
		},
	})
	want := `{"user_id":"u_1","balance":400,"entries":[` +
		`{"kind":"credit","amount":500,"payment_id":"pay_1","event_id":"evt_1"},` +
		`{"kind":"spend","amount":-100,"payment_id":null,"event_id":null}]}`
	if string(b) != want {
		t.Fatalf("ledger body = %s", b)
	}
}

func TestEmptyLedgerIsAnArrayNotNull(t *testing.T) {
	b := mustMarshal(ledgerResponse{UserID: "u_nobody", Entries: []entryResponse{}})
	if string(b) != `{"user_id":"u_nobody","balance":0,"entries":[]}` {
		t.Fatalf("empty ledger body = %s; a user with no movements has an empty ledger, not a null one", b)
	}
}

// PostgreSQL stores `response_body` as jsonb, which preserves the value and not
// the bytes: a body written as {"user_id":…,"balance":…} comes back with its
// keys reordered. A replay has to be the answer the first call gave, so it is
// re-rendered rather than echoed.
func TestReplayedBodyIsCanonicalisedBackToTheOriginalBytes(t *testing.T) {
	asStoredByPostgres := []byte(`{"balance": 400, "user_id": "u_1"}`)

	got := string(canonicalWalletBody(asStoredByPostgres))
	want := `{"user_id":"u_1","balance":400}`
	if got != want {
		t.Fatalf("replayed body = %s, want %s", got, want)
	}
}

func TestReplayedBodyPassesThroughWhatItCannotRead(t *testing.T) {
	stored := []byte(`{"something":"else"}`)
	if got := string(canonicalWalletBody(stored)); got != `{"something":"else"}` {
		t.Fatalf("replayed body = %s, want the stored body untouched", got)
	}
}
