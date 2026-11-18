package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"wallet/internal/store"
)

// maxSpendBody is generous for a body whose only field is an integer, and
// finite so that a client cannot make this service read forever.
const maxSpendBody = 64 << 10

// getWallet answers for any user id, including one that has never been seen,
// and creates nothing while doing so. A wallet is implied by its entries: a
// user with no entries has a balance of 0, which is the truth, and a 404 would
// make a reader ask the provider whether the payment existed.
func (s *Server) getWallet(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")

	balance, err := s.store.Balance(r.Context(), userID)
	if err != nil {
		s.storageUnavailable(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, walletResponse{UserID: userID, Balance: balance})
}

// getEntries is the balance taken apart: one row per movement, oldest first,
// each naming the payment and the provider event that caused it. A spend names
// neither, because nothing at the provider caused it.
func (s *Server) getEntries(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")

	balance, entries, err := s.store.Ledger(r.Context(), userID)
	if err != nil {
		s.storageUnavailable(w, r, err)
		return
	}

	out := ledgerResponse{UserID: userID, Balance: balance, Entries: make([]entryResponse, 0, len(entries))}
	for _, e := range entries {
		out.Entries = append(out.Entries, entryResponse{
			Kind:      e.Kind,
			Amount:    e.Amount,
			PaymentID: e.PaymentID,
			EventID:   e.EventID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// spend deducts credits and answers the new balance.
//
// Order matters here and is the subject of several scenarios: the amount is
// validated before anything is written, the deduction is one conditional write,
// and a refusal returns having changed nothing — no ledger row, no wallet row,
// and no consumed idempotency key.
func (s *Server) spend(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSpendBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidAmount)
		return
	}

	amount, ok := parseAmount(body)
	if !ok {
		writeError(w, http.StatusBadRequest, codeInvalidAmount)
		return
	}

	key := sanitizeRequestID(r.Header.Get("Idempotency-Key"))
	if len(key) > 255 {
		// A key longer than the contract allows is not a key this service will
		// store, and silently ignoring it would make the retry unsafe.
		writeError(w, http.StatusBadRequest, codeInvalidAmount)
		return
	}

	out, err := s.store.Spend(r.Context(), store.SpendParams{
		UserID:      userID,
		Amount:      amount,
		Key:         key,
		RequestHash: requestFingerprint(userID, amount),
		Render: func(balance int64) (int, []byte) {
			return http.StatusOK, mustMarshal(walletResponse{UserID: userID, Balance: balance})
		},
	})
	switch {
	case errors.Is(err, store.ErrInsufficientBalance):
		writeError(w, http.StatusUnprocessableEntity, codeInsufficientBalance)
		return
	case errors.Is(err, store.ErrIdempotencyKeyReused):
		writeError(w, http.StatusUnprocessableEntity, codeIdempotencyKeyReused)
		return
	case err != nil:
		s.storageUnavailable(w, r, err)
		return
	}

	if out.Replayed {
		// The stored answer, re-rendered. It is the ORIGINAL answer — the
		// balance is the one the first call computed, not the one the wallet
		// holds now — and re-rendering makes it byte-identical to what that
		// call sent, which reading the column back does not: `response_body`
		// is jsonb, so PostgreSQL keeps the value and returns it with its keys
		// reordered and re-spaced.
		writeRaw(w, out.Status, canonicalWalletBody(out.Body))
		return
	}

	writeRaw(w, out.Status, out.Body)
}

// canonicalWalletBody re-marshals a stored wallet answer through the same
// marshaller that produced the original. A body it cannot read is passed
// through untouched rather than discarded: a replay that is merely spaced
// differently is still the right answer.
func canonicalWalletBody(stored []byte) []byte {
	var replay walletResponse
	if err := json.Unmarshal(stored, &replay); err != nil || replay.UserID == "" {
		return stored
	}
	return mustMarshal(replay)
}
