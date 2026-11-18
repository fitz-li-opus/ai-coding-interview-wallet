package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
)

// spendRequest keeps `amount` as raw bytes on purpose. Decoding straight into
// an int64 would let `12.5` arrive as 12 and `"100"` arrive as a parse error
// indistinguishable from a missing field, and a float anywhere near money is
// wrong later rather than wrong now.
type spendRequest struct {
	Amount json.RawMessage `json:"amount"`
}

// parseAmount returns the credits to deduct, or false if the request does not
// carry a strictly positive integer. Zero, negative, fractional, exponential,
// textual, absent and out-of-range are all the same answer: INVALID_AMOUNT.
func parseAmount(body []byte) (int64, bool) {
	var req spendRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return 0, false
	}
	if len(req.Amount) == 0 {
		return 0, false
	}

	raw := string(req.Amount)
	if !isJSONInteger(raw) {
		return 0, false
	}

	amount, err := strconv.ParseInt(raw, 10, 64)
	if err != nil { // out of int64 range
		return 0, false
	}
	if amount <= 0 {
		return 0, false
	}
	return amount, true
}

// isJSONInteger reports whether the raw JSON token is an integer literal: no
// quotes, no decimal point, no exponent, no leading plus, no leading zeros.
func isJSONInteger(raw string) bool {
	if raw == "" {
		return false
	}
	i := 0
	if raw[0] == '-' {
		i++
	}
	if i >= len(raw) {
		return false
	}
	// JSON forbids leading zeros, and so does this: "01" is not a number
	// somebody meant.
	if raw[i] == '0' && len(raw)-i > 1 {
		return false
	}
	for ; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return false
		}
	}
	return true
}

// requestFingerprint identifies what an Idempotency-Key stands for. The key
// plus this hash is what tells a retry (same body) from a different request
// that happens to reuse the key.
func requestFingerprint(userID string, amount int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("spend\x00%s\x00%d", userID, amount)))
	return hex.EncodeToString(sum[:])
}
