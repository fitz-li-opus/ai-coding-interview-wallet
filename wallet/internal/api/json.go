package api

import (
	"encoding/json"
	"net/http"
)

// Error codes. They are the whole of a refusal's body: a caller has to branch
// on the reason, and a wallet whose "insufficient balance" is a sentence is a
// wallet whose clients parse sentences.
const (
	codeInvalidAmount        = "INVALID_AMOUNT"
	codeInsufficientBalance  = "INSUFFICIENT_BALANCE"
	codeIdempotencyKeyReused = "IDEMPOTENCY_KEY_REUSED"
	codeStorageUnavailable   = "STORAGE_UNAVAILABLE"
)

// walletResponse answers both "what is the balance" and "what is it now that I
// have spent" — one shape, so there is one thing to learn.
type walletResponse struct {
	UserID  string `json:"user_id"`
	Balance int64  `json:"balance"`
}

type entryResponse struct {
	Kind      string  `json:"kind"`
	Amount    int64   `json:"amount"`
	PaymentID *string `json:"payment_id"`
	EventID   *string `json:"event_id"`
}

type ledgerResponse struct {
	UserID  string          `json:"user_id"`
	Balance int64           `json:"balance"`
	Entries []entryResponse `json:"entries"`
}

type healthResponse struct {
	Status string `json:"status"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		// A payload this package built itself cannot fail to marshal; if it
		// ever does, say so rather than write a half-serialised body.
		writeRaw(w, http.StatusInternalServerError, mustMarshal(errorResponse{Error: codeStorageUnavailable}))
		return
	}
	writeRaw(w, status, body)
}

func writeRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeRaw(w, status, mustMarshal(errorResponse{Error: code}))
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"error":"STORAGE_UNAVAILABLE"}`)
	}
	return b
}
