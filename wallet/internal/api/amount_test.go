package api

import "testing"

// Every row here is a scenario in spec/bdd/api/spend.feature, plus the cases
// the contract implies but no scenario spends a container on.
func TestParseAmount(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int64
		ok   bool
	}{
		{"a positive integer", `{"amount":100}`, 100, true},
		{"one credit", `{"amount":1}`, 1, true},
		{"the whole balance", `{"amount":500}`, 500, true},
		{"int64 max", `{"amount":9223372036854775807}`, 9223372036854775807, true},

		{"zero is not a spend", `{"amount":0}`, 0, false},
		{"negative is not a top-up", `{"amount":-1000}`, 0, false},
		{"fractional is refused rather than rounded", `{"amount":12.5}`, 0, false},
		{"a whole float is still a float", `{"amount":100.0}`, 0, false},
		{"exponent notation is not an integer literal", `{"amount":1e3}`, 0, false},
		{"a numeric string is not a number", `{"amount":"100"}`, 0, false},
		{"absent", `{}`, 0, false},
		{"null", `{"amount":null}`, 0, false},
		{"true", `{"amount":true}`, 0, false},
		{"an object", `{"amount":{"amount":1}}`, 0, false},
		{"an array", `{"amount":[100]}`, 0, false},
		{"leading zero", `{"amount":0100}`, 0, false},
		{"beyond int64", `{"amount":9223372036854775808}`, 0, false},
		{"empty body", ``, 0, false},
		{"not json", `not json`, 0, false},
		{"json but not an object", `[1,2,3]`, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAmount([]byte(tc.body))
			if ok != tc.ok {
				t.Fatalf("parseAmount(%q) ok = %v, want %v", tc.body, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("parseAmount(%q) = %d, want %d", tc.body, got, tc.want)
			}
			if !ok && got != 0 {
				t.Fatalf("parseAmount(%q) returned %d alongside ok=false; a refused amount must not leak a value", tc.body, got)
			}
		})
	}
}

func TestRequestFingerprint(t *testing.T) {
	a := requestFingerprint("u_1", 100)

	if a != requestFingerprint("u_1", 100) {
		t.Fatal("the same request must fingerprint the same way, or a retry looks like a different request")
	}
	if a == requestFingerprint("u_1", 250) {
		t.Fatal("a different amount must fingerprint differently, or IDEMPOTENCY_KEY_REUSED can never be detected")
	}
	if a == requestFingerprint("u_2", 100) {
		t.Fatal("a different user must fingerprint differently: one client's key must not replay another's answer")
	}
	// The separator has to be unambiguous, or ("u_11", 00) and ("u_1", 100)
	// would collide.
	if requestFingerprint("u_11", 0) == requestFingerprint("u_1", 10) {
		t.Fatal("fingerprint fields are ambiguously joined")
	}
}
