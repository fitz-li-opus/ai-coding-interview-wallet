// Package ingest owns everything that happens to a provider event: parsing it,
// deciding whether it has already been applied, and applying it in one
// transaction with the inbox row that makes it exactly-once.
//
// It is the only place that writes credit and reversal ledger rows. The webhook
// endpoint and the reconciler are two ways of arriving here, not two
// implementations of the same rule.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wallet/internal/webhookauth"
)

// Payment is the provider's payment object, as it appears inside an event.
type Payment struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	AmountCents int64  `json:"amount_cents"`
	Credits     int64  `json:"credits"`
	Status      string `json:"status"`
}

// Event is one provider webhook event. `Created` is the event's own time; it is
// NOT the signature timestamp, which the provider recomputes per delivery.
type Event struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Created int64  `json:"created"`
	Data    struct {
		Payment Payment `json:"payment"`
	} `json:"data"`
}

// Outcome is what one delivery of an event did.
type Outcome string

const (
	// OutcomeApplied: this delivery was the one that moved credits.
	OutcomeApplied Outcome = "applied"
	// OutcomeDuplicate: the event had already been applied; nothing changed.
	// It is the outcome of a DELIVERY, never a value stored in the inbox: the
	// event id is the inbox's primary key, so a second row cannot exist.
	OutcomeDuplicate Outcome = "duplicate"
	// OutcomeIgnored: a well-formed event of a type this service does not know.
	// Recorded, acknowledged, acted on in no way.
	OutcomeIgnored Outcome = "ignored"
)

// The two event types this wallet acts on. Everything else is recorded and
// ignored — see ignoreUnknownTypes below.
const (
	TypeSucceeded = "payment.succeeded"
	TypeRefunded  = "payment.refunded"
)

// ErrInvalidPayload is a correctly signed request that is not an event this
// service can act on: not JSON, or missing a field an event needs. It is the
// caller's cue for 400 INVALID_PAYLOAD.
var ErrInvalidPayload = errors.New("ingest: invalid payload")

// maxBody is generous for an event of five fields and small enough that an
// endpoint that adds money cannot be made to allocate.
const maxBody = 64 << 10

// applyBudget bounds the database work of one delivery so that an answer always
// beats the provider's own DELIVERY_TIMEOUT of 5 seconds. Both outcomes are
// retried by the provider, but a 503 it receives is better than a timeout it
// has to wait out: the retry starts sooner and the refusal is visible in its
// delivery log rather than being an absence.
//
// Cancelling a commit that has in fact succeeded is safe here. The event id is
// the inbox's primary key, so the redelivery that follows is recognised as a
// duplicate and credits nothing a second time — which is the same guarantee
// that covers a crash between commit and answer.
const applyBudget = 4 * time.Second

// ParseEvent decodes and validates an event payload.
//
// It is deliberately strict about what it needs and lenient about what it does
// not: an unknown top-level field is fine (the provider may add one tomorrow),
// a missing id is not (the id is the deduplication key, and an event without
// one cannot be applied at most once).
func ParseEvent(raw []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		// A fractional `credits` lands here too: 12.5 does not fit int64, and
		// a wallet that rounds somebody's money is worse than one that refuses
		// it.
		return Event{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}

	switch {
	case ev.ID == "":
		return Event{}, fmt.Errorf("%w: event id is required", ErrInvalidPayload)
	case ev.Type == "":
		return Event{}, fmt.Errorf("%w: event type is required", ErrInvalidPayload)
	case ev.Data.Payment.ID == "":
		return Event{}, fmt.Errorf("%w: data.payment.id is required", ErrInvalidPayload)
	case ev.Data.Payment.UserID == "":
		return Event{}, fmt.Errorf("%w: data.payment.user_id is required", ErrInvalidPayload)
	}

	// Only the types this service acts on need a usable amount. An unknown type
	// is recorded whatever it carries, because the point of recording it is to
	// stop the provider retrying something nobody will act on.
	if ev.Type == TypeSucceeded || ev.Type == TypeRefunded {
		if ev.Data.Payment.Credits <= 0 {
			return Event{}, fmt.Errorf("%w: data.payment.credits must be a positive integer", ErrInvalidPayload)
		}
	}
	return ev, nil
}

// Apply applies one event at most once and reports what this delivery did.
// It writes the inbox row, the ledger row and the balance in a single
// transaction, and returns only after that transaction has committed.
//
// The order inside the transaction is the whole design:
//
//  1. Claim the event — `INSERT ... ON CONFLICT (event_id) DO NOTHING`. This is
//     the serialisation point. Four copies of one event arriving at one instant
//     all reach it; exactly one inserts a row, and the others block until that
//     transaction commits and then see it. No application-level lock is
//     involved, and none would be sound across replicas.
//  2. Nothing inserted means this event was already applied: count the delivery
//     and touch no ledger row. A duplicate is acknowledged, not refused —
//     refusing it would make the provider deliver it another five times.
//  3. An unknown type commits here: recorded, no ledger row, no wallet.
//  4. Otherwise write the ledger row, then fold it into the balance. Both in
//     this transaction, so `wallets.balance` is never observable out of step
//     with `sum(wallet_entries.amount)`, and no ledger row is ever observable
//     without the inbox row that justifies it.
func Apply(ctx context.Context, pool *pgxpool.Pool, ev Event) (Outcome, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	outcome := OutcomeApplied
	if ev.Type != TypeSucceeded && ev.Type != TypeRefunded {
		outcome = OutcomeIgnored
	}

	claim, err := tx.Exec(ctx, `
		INSERT INTO webhook_events (event_id, type, payment_id, status, outcome)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (event_id) DO NOTHING`,
		ev.ID, ev.Type, ev.Data.Payment.ID, nullIfEmpty(ev.Data.Payment.Status), string(outcome))
	if err != nil {
		return "", fmt.Errorf("claim event: %w", err)
	}

	if claim.RowsAffected() == 0 {
		// Already applied. Record that it was handed to us again — this is the
		// only trace a duplicate leaves, because the event id is the primary
		// key and a second row cannot exist.
		if _, err := tx.Exec(ctx,
			`UPDATE webhook_events SET deliveries = deliveries + 1 WHERE event_id = $1`, ev.ID); err != nil {
			return "", fmt.Errorf("count duplicate delivery: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return "", fmt.Errorf("commit duplicate: %w", err)
		}
		return OutcomeDuplicate, nil
	}

	if outcome == OutcomeIgnored {
		if err := tx.Commit(ctx); err != nil {
			return "", fmt.Errorf("commit ignored: %w", err)
		}
		return OutcomeIgnored, nil
	}

	kind, amount := "credit", ev.Data.Payment.Credits
	if ev.Type == TypeRefunded {
		// The refund event carries the same positive `credits` as the credit
		// event did; only its direction differs. Reading the sign from the
		// payload instead of from the type would make a wallet grow on a
		// refund.
		kind, amount = "reversal", -ev.Data.Payment.Credits
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id)
		VALUES ($1, $2, $3, $4, $5)`,
		ev.Data.Payment.UserID, kind, amount, ev.Data.Payment.ID, ev.ID); err != nil {
		return "", fmt.Errorf("write ledger entry: %w", err)
	}

	// A wallet is not a precondition: credits may arrive for a user this
	// service has never heard of. The balance may go negative — a reversal of
	// credits that were already spent is a debt, and a ledger that refuses to
	// say so is a ledger that lies. Spending is what may never go below zero.
	if _, err := tx.Exec(ctx, `
		INSERT INTO wallets (user_id, balance)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE
		   SET balance = wallets.balance + EXCLUDED.balance,
		       updated_at = now()`,
		ev.Data.Payment.UserID, amount); err != nil {
		return "", fmt.Errorf("update balance: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return OutcomeApplied, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// NewHandler returns the `POST /webhooks/payments` handler: verify the
// signature over the raw bytes, then Apply, then answer — in that order, and
// never answering 2xx before the transaction has committed.
func NewHandler(pool *pgxpool.Pool, secret string, tolerance time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), applyBudget)
		defer cancel()

		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil || len(body) > maxBody {
			// A body we could not read whole is a body we cannot verify.
			refuse(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "unreadable request body")
			return
		}

		// Verify first: before the body is parsed, before anything is written.
		if err := webhookauth.Verify(r.Header.Get(webhookauth.HeaderName), body, secret, tolerance, time.Now()); err != nil {
			code := "SIGNATURE_INVALID"
			if webhookauth.ReasonOf(err) == webhookauth.ReasonStale {
				code = "SIGNATURE_TIMESTAMP_OUT_OF_RANGE"
			}
			refuse(w, r, http.StatusBadRequest, code, string(webhookauth.ReasonOf(err)))
			return
		}

		ev, err := ParseEvent(body)
		if err != nil {
			refuse(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", err.Error())
			return
		}

		outcome, err := Apply(ctx, pool, ev)
		if err != nil {
			// We do not know whether the event was applied, so we must not say
			// it was. A non-2xx leaves the provider holding it, and the credits
			// arrive when the database does. This is the recovery path, not a
			// failure to handle one.
			slog.ErrorContext(ctx, "webhook not applied",
				"event_id", ev.ID, "type", ev.Type, "error", err)
			respond(w, http.StatusServiceUnavailable, map[string]string{"error": "STORAGE_UNAVAILABLE"})
			return
		}

		// Past this line the transaction has committed, so a 2xx is a promise
		// that can be kept.
		slog.InfoContext(ctx, "webhook accepted",
			"event_id", ev.ID, "type", ev.Type, "payment_id", ev.Data.Payment.ID,
			"user_id", ev.Data.Payment.UserID, "outcome", string(outcome))
		respond(w, http.StatusOK, map[string]string{"status": string(outcome)})
	})
}

// refuse answers a request that changed nothing. `detail` is logged, never
// returned: a refusal that explains precisely what was wrong with a signature
// is an oracle.
func refuse(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	slog.WarnContext(r.Context(), "webhook refused", "code", code, "detail", detail)
	respond(w, status, map[string]string{"error": code})
}

func respond(w http.ResponseWriter, status int, body map[string]string) {
	payload, err := json.Marshal(body)
	if err != nil {
		http.Error(w, `{"error":"INTERNAL"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
