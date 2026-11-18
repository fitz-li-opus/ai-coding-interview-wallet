// Package reconcile closes the hole the provider leaves behind: it gives up
// after six delivery attempts, and it has no API for querying a single payment.
// The only recovery path is its event list.
package reconcile

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"wallet/internal/ingest"
	"wallet/internal/provider"
)

// Result is what one reconciliation pass did.
type Result struct {
	Listed  int `json:"listed"`  // events the provider reported
	Applied int `json:"applied"` // events that were missing here and have now been applied
	Skipped int `json:"skipped"` // events already in the inbox
}

// ErrNotImplemented is returned by the stub below until it is written.
//
// Kept because it is part of this package's published surface; nothing returns
// it now.
var ErrNotImplemented = errors.New("reconcile: not implemented")

// Run makes one pass: list the provider's events, apply the ones this service
// has no inbox row for, and leave everything else alone. It is idempotent —
// running it twice applies nothing the second time — because it arrives at the
// same exactly-once path the webhook endpoint uses.
//
// Idempotence has two layers, and only the second one is load-bearing:
//
//   - The inbox is read first, so an event already recorded is never handed to
//     ingest at all. This is what makes Skipped a meaningful number; it is an
//     optimisation, not a guarantee, because a delivery can land between that
//     read and the write.
//   - ingest.Apply is exactly-once on its own: the provider's event id is the
//     primary key of the inbox row it writes in the same transaction as the
//     ledger row. So a delivery racing this pass over the same event produces
//     one ledger row, whichever of them commits first — and the loser is told
//     it was a duplicate, which this pass counts as skipped.
//
// Nothing here looks at delivery attempts. An event missing from the inbox is
// applied whether or not the provider has given up on it, because waiting for
// it to give up would leave credits unapplied for no benefit: a late delivery
// of an event this pass already applied is a duplicate, and duplicates are
// free.
//
// A note on counting: an event of a type this service does not know is recorded
// and acted on in no way, which still means this pass wrote its inbox row, so
// it counts as Applied. The real provider only emits the two types this service
// knows, so that case belongs to a future provider rather than to this one.
func Run(ctx context.Context, pool *pgxpool.Pool, providerBaseURL string) (Result, error) {
	events, err := provider.New(providerBaseURL).Events(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list provider events: %w", err)
	}

	result := Result{Listed: len(events)}
	if len(events) == 0 {
		return result, nil
	}

	known, err := knownEventIDs(ctx, pool, eventIDs(events))
	if err != nil {
		return result, fmt.Errorf("read inbox: %w", err)
	}

	for _, event := range missing(events, known) {
		outcome, err := ingest.Apply(ctx, pool, asIngestEvent(event))
		if err != nil {
			// Partial progress is real and is reported: the events applied
			// before this one are committed, and the next pass will pick up
			// from where this one stopped.
			return result, fmt.Errorf("apply event %s: %w", event.ID, err)
		}
		switch outcome {
		case ingest.OutcomeDuplicate:
			// A delivery committed between the inbox read and here.
			result.Skipped++
		default:
			result.Applied++
		}
	}
	// Everything the provider listed was either applied by this pass or left
	// alone, so what was not applied was skipped — whether it was skipped by
	// the inbox read or by ingest telling us it was a duplicate.
	result.Skipped = len(events) - result.Applied
	return result, nil
}

// missing returns the events with no inbox row, in the order the provider
// reported them — oldest first, so a credit is applied before the refund that
// reverses it even though the ledger would reach the same balance either way.
func missing(events []provider.Event, known map[string]bool) []provider.Event {
	out := make([]provider.Event, 0, len(events))
	for _, event := range events {
		if !known[event.ID] {
			out = append(out, event)
		}
	}
	return out
}

// eventIDs collects the ids to ask the inbox about.
func eventIDs(events []provider.Event) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}

// knownEventIDs returns which of ids already have an inbox row. It asks about
// the provider's list rather than reading the whole inbox, so the query is
// bounded by what the provider remembers.
func knownEventIDs(ctx context.Context, pool *pgxpool.Pool, ids []string) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT event_id FROM webhook_events WHERE event_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	known := make(map[string]bool, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		known[id] = true
	}
	return known, rows.Err()
}

// asIngestEvent converts the provider's wire shape into the event ingest
// applies. The two types are deliberately separate: this is the seam where a
// counterparty's format stops being this service's business.
func asIngestEvent(event provider.Event) ingest.Event {
	out := ingest.Event{
		ID:      event.ID,
		Type:    event.Type,
		Created: event.Created,
	}
	out.Data.Payment = ingest.Payment{
		ID:          event.Data.Payment.ID,
		UserID:      event.Data.Payment.UserID,
		AmountCents: event.Data.Payment.AmountCents,
		Credits:     event.Data.Payment.Credits,
		Status:      event.Data.Payment.Status,
	}
	return out
}
