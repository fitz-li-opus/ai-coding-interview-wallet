// Package webhookauth verifies the provider's `Opus-Signature` header.
//
// This is the whole of the authentication on `POST /webhooks/payments`. The
// endpoint is public and it adds money, so nothing else about the request is
// evidence: not the source address, not the shape of the JSON, not the `pay_`
// prefix on a payment id.
//
// Three properties are not negotiable, and each one is a way this check is
// usually got wrong:
//
//   - The digest covers THE BYTES THAT ARRIVED. The provider signs
//     `"<t>." + <raw body>`, where the body is whatever its JSON encoder
//     produced. Verifying against a re-encoded copy of the parsed object
//     compares our key order and spacing with theirs, and fails the moment
//     they differ — a bug that looks like a key mismatch and is not one.
//   - The comparison is CONSTANT TIME. A byte-by-byte compare that returns
//     early leaks the expected digest one byte at a time.
//   - The timestamp is checked SECOND, and only once the digest is good.
//     Checking it first tells an attacker which of their two problems to fix,
//     and reports a forgery as a clock problem.
package webhookauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HeaderName is the header the provider signs with.
const HeaderName = "Opus-Signature"

// DefaultTolerance is how far `t` may be from now in either direction.
//
// 300s is comfortably wider than the provider's entire retry sequence
// (1, 2, 4, 8, 16 seconds), so a legitimate retry is never refused for being
// late; and narrow enough that a captured request stops being replayable in
// five minutes rather than never. `t` is re-stamped on every attempt, so this
// window applies per delivery and not to the event.
const DefaultTolerance = 300 * time.Second

// Reason says which rule a request broke. The caller maps it to a status code
// and an error code; it is deliberately coarse, because a refusal that explains
// itself precisely is an oracle.
type Reason string

const (
	// ReasonMissing: no signature header at all.
	ReasonMissing Reason = "missing"
	// ReasonMalformed: a header this service cannot parse — not `t=<int>` and
	// `v1=<hex>`.
	ReasonMalformed Reason = "malformed"
	// ReasonMismatch: a well-formed header whose digest is not the digest of
	// these bytes under this secret. A wrong key and a body edited after
	// signing are both this.
	ReasonMismatch Reason = "mismatch"
	// ReasonStale: the digest verifies, but `t` is outside the tolerance
	// window. The only reason that implies the request was once genuine.
	ReasonStale Reason = "stale"
)

// Error is a refusal, carrying the Reason and a message that is safe to log.
// The message never contains the secret, the expected digest, or any prefix of
// it.
type Error struct {
	Reason Reason
	detail string
}

func (e *Error) Error() string { return fmt.Sprintf("webhookauth: %s (%s)", e.Reason, e.detail) }

// ReasonOf returns the Reason of err, or "" if err is not a webhookauth error.
func ReasonOf(err error) Reason {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ""
}

// Sign produces the header value the provider would send for these bytes at
// this timestamp. The service never sends a webhook, so this exists for tests
// and for the `--self-check` path: one implementation of the algorithm, used by
// both directions, is the only way the test proves anything about the verifier.
func Sign(body []byte, secret string, t time.Time) string {
	ts := t.Unix()
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(digest(body, secret, ts)))
}

func digest(body []byte, secret string, ts int64) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	// "<t>." + raw body — concatenated, never re-encoded.
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return mac.Sum(nil)
}

// Verify checks header against the exact bytes of body. It returns nil when the
// request is authentic and inside the tolerance window, and an *Error
// otherwise.
func Verify(header string, body []byte, secret string, tolerance time.Duration, now time.Time) error {
	if strings.TrimSpace(header) == "" {
		return &Error{Reason: ReasonMissing, detail: "no " + HeaderName + " header"}
	}

	var (
		tsRaw, sigRaw string
		seenT, seenV1 bool
	)
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return &Error{Reason: ReasonMalformed, detail: "element is not key=value"}
		}
		switch strings.TrimSpace(key) {
		case "t":
			if seenT {
				return &Error{Reason: ReasonMalformed, detail: "t given twice"}
			}
			tsRaw, seenT = strings.TrimSpace(value), true
		case "v1":
			if seenV1 {
				return &Error{Reason: ReasonMalformed, detail: "v1 given twice"}
			}
			sigRaw, seenV1 = strings.TrimSpace(value), true
		default:
			// Unknown elements are ignored, not refused: a provider that adds a
			// v2 digest alongside v1 must not break a verifier that only knows
			// v1.
		}
	}
	if !seenT || !seenV1 {
		return &Error{Reason: ReasonMalformed, detail: "t and v1 are both required"}
	}

	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return &Error{Reason: ReasonMalformed, detail: "t is not a unix timestamp"}
	}
	provided, err := hex.DecodeString(sigRaw)
	if err != nil || len(provided) != sha256.Size {
		return &Error{Reason: ReasonMalformed, detail: "v1 is not a sha256 digest in hex"}
	}

	// The digest decides authenticity; the clock only decides freshness. In
	// this order, a forgery is never reported as a clock problem.
	if !hmac.Equal(provided, digest(body, secret, ts)) {
		return &Error{Reason: ReasonMismatch, detail: "digest does not match these bytes"}
	}

	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	if skew := now.Sub(time.Unix(ts, 0)); skew > tolerance || skew < -tolerance {
		return &Error{Reason: ReasonStale, detail: "t is outside the tolerance window"}
	}
	return nil
}
