package bdd

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
)

// The counterparty's half of the registry: the real payment provider acting,
// and the one step that speaks in its place.
//
// The line between the two is the rule this file exists to hold. Every delivery
// a wallet is supposed to accept is made by `services/payment-provider` — it
// mints the event, it signs it, it retries it, and it records what we answered.
// The harness only sends what that provider can never send: a forgery, a
// backdated timestamp, a broken header, a body that is not JSON, or two real
// events in an order the provider would not have chosen. A happy path written
// with the harness step would be asserting that the harness and the wallet
// agree about a signature, which is the one way this suite could go green while
// the wallet is wrong.

// providerClient talks to the provider's control API. Its timeout is per
// request and generous: a resend is answered immediately (the delivery happens
// on the provider's own thread), and `GET /events` is a dump of a map.
var providerClient = &http.Client{Timeout: 10 * time.Second}

// wrongSecret is a key nobody issued, for the forgery variant. It must be a
// valid secret in shape so that only the digest, and nothing about the request,
// is what the wallet objects to.
const wrongSecret = "whsec_not_the_interview_secret"

// staleBy backdates the `with a stale timestamp` variant well past any
// plausible WEBHOOK_TOLERANCE_SECONDS (300 by default), so the scenario is
// about the window existing rather than about where its edge is.
const staleBy = 1 * time.Hour

// resendExpect tracks, per stack, how many delivery attempts the scenario has
// asked the provider to make. `these things happen at one instant:` runs four
// copies of the redelivery step concurrently, and each one has to wait for all
// four attempts rather than for the first one it happens to see — otherwise the
// step returns while attempts are still landing and the count that follows is a
// race. The counter is cumulative, so concurrent callers converge on the same
// target.
var resendExpect = struct {
	sync.Mutex
	total map[*Stack]int
}{total: map[*Stack]int{}}

// RegisterProviderSteps registers its share of the registry. See steps.go.
func RegisterProviderSteps(sc *godog.ScenarioContext, w *World) {
	sc.Step(`^payment provider creates a payment as "([a-z][a-zA-Z0-9]+)":$`,
		func(ctx context.Context, varName string, doc *godog.DocString) error {
			return createPayment(ctx, w, varName, doc.Content)
		})

	sc.Step(`^payment provider refunds payment "([^"]+)"$`,
		func(ctx context.Context, paymentID string) error {
			id, err := w.Subst(paymentID)
			if err != nil {
				return err
			}
			return refundPayment(ctx, w, id)
		})

	sc.Step(`^payment provider redelivers each event ([0-9]+) times?$`,
		func(ctx context.Context, times int) error {
			return redeliverEachEvent(ctx, w, times)
		})

	sc.Step(`^exactly ([0-9]+) webhook deliver(?:y|ies) (?:was|were) (accepted|refused)$`,
		func(ctx context.Context, count int, outcome string) error {
			return countDeliveries(ctx, w, count, outcome)
		})

	sc.Step(`^a webhook is delivered by the harness(?:, (unsigned|signed with the wrong secret|tampered after signing|with a stale timestamp|with a malformed signature header|with a malformed body))?:$`,
		func(ctx context.Context, variant string, doc *godog.DocString) error {
			return deliverHarnessWebhook(ctx, w, variant, doc.Content)
		})
}

// ── The provider acting ──────────────────────────────────────────────────────

// createPayment asks the provider for a payment and saves the id it minted.
// The id is the only thing a scenario ever needs from this answer, and it
// cannot be guessed: the provider builds it from a random uuid.
func createPayment(ctx context.Context, w *World, varName, body string) error {
	payload, err := w.Subst(body)
	if err != nil {
		return err
	}
	payload, err = numberFields(payload, "amount_cents")
	if err != nil {
		return err
	}

	resp, err := providerDo(ctx, w, http.MethodPost, "/payments", []byte(payload))
	if err != nil {
		return err
	}
	if resp.Status < 200 || resp.Status > 299 {
		return fmt.Errorf("payment provider refused to create a payment: %d %s", resp.Status, resp.Body)
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &created); err != nil {
		return fmt.Errorf("payment provider's answer is not a payment: %w (body %s)", err, resp.Body)
	}
	if created.ID == "" {
		return fmt.Errorf("payment provider answered without a payment id: %s", resp.Body)
	}

	w.Vars[varName] = created.ID
	// The provider's answer is its own claim, not the wallet's. Recording it
	// lets a scenario assert `response status is 201` and nothing more.
	w.Record(resp)

	// The event exists the moment the payment does; its delivery is a thread
	// the provider starts a second later.
	noteEvents(ctx, w)
	return nil
}

// numberFields re-types the named top-level fields as JSON numbers when the
// docstring carried them as strings.
//
// This is the send-side half of a convention format.yml states for docstrings:
// a Scenario Outline token occupying a whole JSON value is written quoted
// (`"amount_cents": "<amount>"`), because `must_be_valid_json_after_substitution`
// requires the file to parse as JSON before the Examples table is applied.
// Godog substitutes the column into that string position, so the step receives
// `"500"` where the payment provider's contract declares an integer — and the
// provider is strict about it (`type(amount_cents) is int`), rightly, since a
// payment amount arriving as text is exactly the bug it should refuse.
//
// Only fields the counterparty's contract declares as integers are converted,
// never "every string that looks like a number": a `user_id` of "123" is a
// string, and guessing from the shape of a value is how an id becomes a number
// in the one scenario nobody checks. The assertion side of the same convention
// lives in jsonSubset, where a quoted scalar matches an unquoted one.
func numberFields(payload string, fields ...string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		// Not an object: nothing to re-type, and whatever this is the
		// counterparty gets to refuse it on its own terms.
		return payload, nil
	}

	changed := false
	for _, field := range fields {
		text, ok := obj[field].(string)
		if !ok {
			continue
		}
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return "", fmt.Errorf("%q must be an integer, got %q", field, text)
		}
		obj[field] = json.Number(text)
		changed = true
	}
	if !changed {
		return payload, nil
	}

	out, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// refundPayment refunds in full — the provider has no amount parameter, so
// partial refunds do not exist in this domain. It expects the provider to
// accept: its own guard against a second refund (409) and an unknown payment
// (404) is not a claim any sentence of this registry can make, and format.yml
// records that as a known gap rather than papering over it.
func refundPayment(ctx context.Context, w *World, paymentID string) error {
	resp, err := providerDo(ctx, w, http.MethodPost, "/payments/"+paymentID+"/refund", nil)
	if err != nil {
		return err
	}
	if resp.Status < 200 || resp.Status > 299 {
		return fmt.Errorf("payment provider refused to refund %s: %d %s", paymentID, resp.Status, resp.Body)
	}
	noteEvents(ctx, w)
	return nil
}

// redeliverEachEvent presses the provider's Resend button n times for every
// event it holds. A resend is one attempt with no retry loop of its own, which
// is what makes the count that follows exact — and it is why DUPLICATE_RATE is
// 0 everywhere else: idempotency asserted by a coin flip is idempotency
// asserted sometimes.
//
// It returns once the provider has recorded every attempt it was asked for, so
// the next step reads a settled log.
func redeliverEachEvent(ctx context.Context, w *World, times int) error {
	events, err := w.Stack.ProviderEvents(ctx)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("payment provider holds no event to redeliver")
	}
	noteEventsFrom(w, events)

	target := claimAttempts(w.Stack, totalAttempts(events), len(events)*times)

	// The copies of one event are released together: this is the shape of
	// at-least-once delivery, not a queue being drained.
	var wg sync.WaitGroup
	errs := make(chan error, len(events)*times)
	for _, ev := range events {
		for i := 0; i < times; i++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				resp, err := providerDo(ctx, w, http.MethodPost, "/events/"+id+"/resend", nil)
				switch {
				case err != nil:
					errs <- err
				case resp.Status < 200 || resp.Status > 299:
					errs <- fmt.Errorf("payment provider refused to resend %s: %d %s", id, resp.Status, resp.Body)
				}
			}(ev.ID)
		}
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return err
	}

	// This step records NO response. The deliveries it causes are made by the
	// provider as an HTTP client, so what the wallet answered is the
	// provider's observation and is read from the provider's own log.
	return waitForAttempts(ctx, w, target)
}

// countDeliveries reads the provider's own attempt log. Some claims can only be
// made from the far side of the wire: that the provider stopped retrying is the
// only observable difference between a wallet that committed before answering
// and one that answered first.
func countDeliveries(ctx context.Context, w *World, want int, outcome string) error {
	events, err := w.Stack.ProviderEvents(ctx)
	if err != nil {
		return err
	}
	noteEventsFrom(w, events)

	accepted, refused := tallyAttempts(events)
	got := accepted
	if outcome == "refused" {
		got = refused
	}
	if got != want {
		return fmt.Errorf("expected exactly %d webhook deliveries %s, provider recorded %d\n%s",
			want, outcome, got, describeAttempts(events))
	}
	return nil
}

// ── The harness speaking ─────────────────────────────────────────────────────

// deliverHarnessWebhook builds one webhook from the docstring, signs it the way
// the provider signs every attempt, breaks it if a variant says so, and POSTs
// it to the wallet. It records the wallet's answer, so `response status is`
// reads it directly and no scenario using this step waits for anything.
func deliverHarnessWebhook(ctx context.Context, w *World, variant, body string) error {
	payload, err := w.Subst(body)
	if err != nil {
		return err
	}

	send := []byte(payload)
	secret := w.Stack.WebhookSecret
	timestamp := time.Now().Unix()
	header := ""

	switch variant {
	case "":
		// Correctly signed and current. The form used for claims about ORDER
		// and SHAPE: a refund before its credit, a type that does not exist
		// yet, a payload with a field missing.
		header = signature(secret, timestamp, send)

	case "unsigned":
		// No header at all — the simplest attack on an endpoint that mints
		// credits.

	case "signed with the wrong secret":
		header = signature(wrongSecret, timestamp, send)

	case "tampered after signing":
		// Sign the original, then raise the credits. The digest no longer
		// matches what is sent, which is exactly the attack this endpoint
		// exists to refuse — and the reason the signature must cover the bytes
		// the handler parses rather than ones it re-serialises.
		header = signature(secret, timestamp, send)
		if send, err = raiseCredits(send); err != nil {
			return err
		}

	case "with a stale timestamp":
		// The only variant whose digest verifies. Every real delivery carries
		// a fresh `t`, so a wallet that ignores the timestamp passes every
		// other scenario in this suite and fails only here.
		timestamp = time.Now().Add(-staleBy).Unix()
		header = signature(secret, timestamp, send)

	case "with a malformed signature header":
		// Present, but not `t=…,v1=…`. A parser that throws here is a 500
		// where a 400 belongs.
		header = fmt.Sprintf("t=%d", timestamp)

	case "with a malformed body":
		// Sign the bytes that are actually sent, then send bytes that are not
		// JSON. Because the digest still matches, the refusal can only be
		// about the body — which is how the scenario states the order of
		// operations: verify, then parse.
		send = malformJSON(send)
		header = signature(secret, timestamp, send)

	default:
		return fmt.Errorf("unknown harness webhook variant %q", variant)
	}

	headers := map[string]string{"Content-Type": "application/json"}
	if header != "" {
		headers["Opus-Signature"] = header
	}

	resp, err := w.Do(ctx, http.MethodPost, "/webhooks/payments", headers, send)
	if err != nil {
		return err
	}
	w.Record(resp)
	return nil
}

// signature is the provider's scheme, byte for byte:
// HMAC-SHA256(secret, "<t>." + raw body), hex, in `t=…,v1=…`.
func signature(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))
}

// raiseCredits doubles the credits the event claims, or invents a figure if the
// payload has none. It is the only thing the tampering changes.
func raiseCredits(body []byte) ([]byte, error) {
	var event map[string]any
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("tampering needs a JSON event: %w", err)
	}
	data, _ := event["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
		event["data"] = data
	}
	payment, _ := data["payment"].(map[string]any)
	if payment == nil {
		payment = map[string]any{}
		data["payment"] = payment
	}

	raised := float64(999999)
	if current, ok := payment["credits"].(float64); ok {
		raised = current * 2
	}
	payment["credits"] = raised

	tampered, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(tampered, body) {
		return nil, fmt.Errorf("tampering changed nothing, which would assert nothing")
	}
	return tampered, nil
}

// malformJSON returns bytes that are not JSON by any reading: the payload cut
// in half.
func malformJSON(body []byte) []byte {
	if len(body) < 4 {
		return []byte("{")
	}
	return body[:len(body)/2]
}

// ── Reading the counterparty ─────────────────────────────────────────────────

// providerDo performs one request against this scenario's payment provider.
func providerDo(ctx context.Context, w *World, method, path string, body []byte) (*Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(w.Stack.ProviderURL, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := providerClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("payment provider %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Body: answer, Header: resp.Header.Clone()}, nil
}

// attemptAccepted reports whether the provider logged a 2xx for that attempt.
// Anything else — a 4xx, a 5xx, or the string it stores when there was no
// answer at all — is a refusal.
func attemptAccepted(a DeliveryAttempt) bool {
	switch status := a.Status.(type) {
	case float64:
		return status >= 200 && status < 300
	case int:
		return status >= 200 && status < 300
	case json.Number:
		n, err := status.Int64()
		return err == nil && n >= 200 && n < 300
	default:
		return false
	}
}

func tallyAttempts(events []ProviderEvent) (accepted, refused int) {
	for _, ev := range events {
		for _, a := range ev.Attempts {
			if attemptAccepted(a) {
				accepted++
				continue
			}
			refused++
		}
	}
	return accepted, refused
}

func totalAttempts(events []ProviderEvent) int {
	accepted, refused := tallyAttempts(events)
	return accepted + refused
}

// describeAttempts is what a failing count prints. Without it, every failure in
// this file is a guessing game about which event was delivered how often.
func describeAttempts(events []ProviderEvent) string {
	if len(events) == 0 {
		return "payment provider holds no events"
	}
	var b strings.Builder
	b.WriteString("payment provider's delivery log:\n")
	for _, ev := range events {
		fmt.Fprintf(&b, "  %s  %-18s payment=%s attempts=[", ev.ID, ev.Type, ev.Data.Payment.ID)
		for i, a := range ev.Attempts {
			if i > 0 {
				b.WriteString(" ")
			}
			fmt.Fprintf(&b, "%v", a.Status)
		}
		b.WriteString("]\n")
	}
	return b.String()
}

// claimAttempts reserves a cumulative attempt target for one redelivery step
// and returns the total every caller must now wait for.
//
// It ADDS this step's own copies to the running target rather than raising the
// target to them, which is the whole point under `these things happen at one
// instant:`. Four concurrent copies each observe the same one attempt already
// in the log; if each only raised the target to "observed + my 1", all four
// would wait for the first attempt to land and return with three still in
// flight. Accumulating makes them wait for all four.
//
// `observed` floors the target, so attempts the provider made on its own —
// retries into a wallet that was down — are counted in rather than mistaken
// for copies this step asked for.
func claimAttempts(stack *Stack, observed, delta int) int {
	resendExpect.Lock()
	defer resendExpect.Unlock()

	target := resendExpect.total[stack]
	if observed > target {
		target = observed
	}
	target += delta
	resendExpect.total[stack] = target
	return target
}

// waitForAttempts waits until the provider has recorded at least target
// attempts in total.
func waitForAttempts(ctx context.Context, w *World, target int) error {
	deadline := time.Now().Add(redeliverTimeout)
	for {
		events, err := w.Stack.ProviderEvents(ctx)
		if err != nil {
			return err
		}
		if totalAttempts(events) >= target {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("payment provider recorded %d delivery attempts, expected at least %d within %s\n%s",
				totalAttempts(events), target, redeliverTimeout, describeAttempts(events))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(settlePollInterval):
		}
	}
}

// noteEvents refreshes the scenario's record of which provider events exist.
func noteEvents(ctx context.Context, w *World) {
	events, err := w.Stack.ProviderEvents(ctx)
	if err != nil {
		return
	}
	noteEventsFrom(w, events)
}

func noteEventsFrom(w *World, events []ProviderEvent) {
	known := make(map[string]bool, len(w.Events))
	for _, id := range w.Events {
		known[id] = true
	}
	for _, ev := range events {
		if !known[ev.ID] {
			w.Events = append(w.Events, ev.ID)
			known[ev.ID] = true
		}
	}
}
