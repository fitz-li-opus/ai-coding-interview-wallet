package webhookauth

import (
	"strings"
	"testing"
	"time"
)

// The vector below was produced by the provider's own algorithm, independently
// of this package:
//
//	signed = f"{t}.".encode() + body
//	hmac.new(secret, signed, hashlib.sha256).hexdigest()
//
// That is the point of hard-coding it. A test that signs with Sign and then
// verifies with Verify proves the two halves of this file agree with each
// other; it proves nothing about agreeing with services/payment-provider.
const (
	vecSecret  = "whsec_interview_test"
	vecOther   = "whsec_other_secret"
	vecTS      = int64(1767225601)
	vecBody    = `{"id": "evt_8f3a1c2b9d4e5f60", "type": "payment.succeeded", "created": 1767225600, "data": {"payment": {"id": "pay_1a2b3c4d5e6f7a8b", "user_id": "u_1", "amount_cents": 500, "credits": 500, "status": "succeeded"}}}`
	vecV1      = "905d85d3b552432921599b738f0f9b74f0dcb971e6efb8481185b4c895f57caa"
	vecV1Other = "2466faee5ef2ed5c99532961ce923acda2eb18dba3700d2b556cd32833917e9b"
)

func vecNow() time.Time { return time.Unix(vecTS, 0) }

func TestVerifyAcceptsTheProvidersOwnDigest(t *testing.T) {
	header := "t=" + itoa(vecTS) + ",v1=" + vecV1
	if err := Verify(header, []byte(vecBody), vecSecret, DefaultTolerance, vecNow()); err != nil {
		t.Fatalf("the provider's own signature was refused: %v", err)
	}
}

// Sign must produce exactly what the provider produces, because the harness and
// any self-check depend on it being the same algorithm.
func TestSignMatchesTheProvidersDigest(t *testing.T) {
	got := Sign([]byte(vecBody), vecSecret, vecNow())
	want := "t=" + itoa(vecTS) + ",v1=" + vecV1
	if got != want {
		t.Fatalf("Sign disagrees with the provider\n got: %s\nwant: %s", got, want)
	}
}

// Verifying against a re-encoded copy of the parsed object is the classic bug:
// the same event, re-serialised with no spaces, must NOT verify under the
// digest of the bytes that actually arrived.
func TestVerifyIsOverRawBytesNotReencodedJSON(t *testing.T) {
	compact := strings.ReplaceAll(strings.ReplaceAll(vecBody, `": `, `":`), `, "`, `,"`)
	if compact == vecBody {
		t.Fatal("test is broken: the compact form is identical")
	}
	header := "t=" + itoa(vecTS) + ",v1=" + vecV1
	if err := Verify(header, []byte(compact), vecSecret, DefaultTolerance, vecNow()); ReasonOf(err) != ReasonMismatch {
		t.Fatalf("re-encoded body: want mismatch, got %v", err)
	}
}

func TestVerifyRefusals(t *testing.T) {
	good := "t=" + itoa(vecTS) + ",v1=" + vecV1

	cases := []struct {
		name   string
		header string
		body   string
		secret string
		now    time.Time
		want   Reason
	}{
		{"no header", "", vecBody, vecSecret, vecNow(), ReasonMissing},
		{"whitespace only", "   ", vecBody, vecSecret, vecNow(), ReasonMissing},
		{"not key=value", "garbage", vecBody, vecSecret, vecNow(), ReasonMalformed},
		{"v1 missing", "t=" + itoa(vecTS), vecBody, vecSecret, vecNow(), ReasonMalformed},
		{"t missing", "v1=" + vecV1, vecBody, vecSecret, vecNow(), ReasonMalformed},
		{"t not a number", "t=yesterday,v1=" + vecV1, vecBody, vecSecret, vecNow(), ReasonMalformed},
		{"v1 not hex", "t=" + itoa(vecTS) + ",v1=zzzz", vecBody, vecSecret, vecNow(), ReasonMalformed},
		{"v1 truncated", "t=" + itoa(vecTS) + ",v1=" + vecV1[:32], vecBody, vecSecret, vecNow(), ReasonMalformed},
		{"t given twice", "t=1,t=2,v1=" + vecV1, vecBody, vecSecret, vecNow(), ReasonMalformed},
		{"signed with another secret", "t=" + itoa(vecTS) + ",v1=" + vecV1Other, vecBody, vecSecret, vecNow(), ReasonMismatch},
		{"our digest, their secret", good, vecBody, vecOther, vecNow(), ReasonMismatch},
		{"tampered after signing", good, strings.Replace(vecBody, `"credits": 500`, `"credits": 999`, 1), vecSecret, vecNow(), ReasonMismatch},
		{"empty body", good, "", vecSecret, vecNow(), ReasonMismatch},
		{"too old", good, vecBody, vecSecret, vecNow().Add(DefaultTolerance + time.Second), ReasonStale},
		{"too far in the future", good, vecBody, vecSecret, vecNow().Add(-(DefaultTolerance + time.Second)), ReasonStale},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify(tc.header, []byte(tc.body), tc.secret, DefaultTolerance, tc.now)
			if got := ReasonOf(err); got != tc.want {
				t.Fatalf("want reason %q, got %q (err=%v)", tc.want, got, err)
			}
		})
	}
}

// The edges of the window are inside it: the provider's last retry lands 31
// seconds after the first attempt, and a refusal there would lose a payment.
func TestVerifyToleranceEdgesAreInclusive(t *testing.T) {
	good := "t=" + itoa(vecTS) + ",v1=" + vecV1
	for _, skew := range []time.Duration{0, 31 * time.Second, DefaultTolerance, -DefaultTolerance} {
		if err := Verify(good, []byte(vecBody), vecSecret, DefaultTolerance, vecNow().Add(skew)); err != nil {
			t.Fatalf("skew %v should be accepted: %v", skew, err)
		}
	}
}

// A refusal must not hand back the digest it wanted, nor the secret.
func TestRefusalIsNotAnOracle(t *testing.T) {
	err := Verify("t="+itoa(vecTS)+",v1="+vecV1Other, []byte(vecBody), vecSecret, DefaultTolerance, vecNow())
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	for _, leak := range []string{vecSecret, vecV1, vecV1[:8]} {
		if strings.Contains(msg, leak) {
			t.Fatalf("error message leaks %q: %s", leak, msg)
		}
	}
}

// Unknown elements are ignored so that a provider adding a v2 digest does not
// break a verifier that only knows v1.
func TestVerifyIgnoresUnknownElements(t *testing.T) {
	header := "t=" + itoa(vecTS) + ",v1=" + vecV1 + ",v2=whatever"
	if err := Verify(header, []byte(vecBody), vecSecret, DefaultTolerance, vecNow()); err != nil {
		t.Fatalf("unknown element should be ignored: %v", err)
	}
}

func itoa(i int64) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	return string(b)
}
