package reconcile

import (
	"testing"

	"wallet/internal/provider"
)

func event(id, typ, paymentID string, credits int64, status string) provider.Event {
	ev := provider.Event{ID: id, Type: typ, Created: 1767225600}
	ev.Data.Payment = provider.Payment{
		ID:          paymentID,
		UserID:      "u_1",
		AmountCents: credits,
		Credits:     credits,
		Status:      status,
	}
	return ev
}

func TestMissingSelectsOnlyEventsWithNoInboxRow(t *testing.T) {
	events := []provider.Event{
		event("evt_1", "payment.succeeded", "pay_1", 500, "succeeded"),
		event("evt_2", "payment.refunded", "pay_1", 500, "refunded"),
		event("evt_3", "payment.succeeded", "pay_2", 200, "succeeded"),
	}
	known := map[string]bool{"evt_2": true}

	got := missing(events, known)

	if len(got) != 2 {
		t.Fatalf("got %d missing events, want 2", len(got))
	}
	// Order is the provider's order, oldest first.
	if got[0].ID != "evt_1" || got[1].ID != "evt_3" {
		t.Fatalf("got %s and %s, want evt_1 then evt_3", got[0].ID, got[1].ID)
	}
}

func TestMissingIsEmptyWhenTheInboxIsComplete(t *testing.T) {
	events := []provider.Event{
		event("evt_1", "payment.succeeded", "pay_1", 500, "succeeded"),
		event("evt_2", "payment.refunded", "pay_1", 500, "refunded"),
	}
	known := map[string]bool{"evt_1": true, "evt_2": true}

	if got := missing(events, known); len(got) != 0 {
		t.Fatalf("got %d missing events, want none — a second pass must find nothing to do", len(got))
	}
}

func TestMissingTakesNothingOnTrustFromDeliveryAttempts(t *testing.T) {
	// An event the provider is still retrying, and one it gave up on, are the
	// same to reconciliation: neither has an inbox row, so both are applied.
	// Duplicates are free; unapplied credits are not.
	stillTrying := event("evt_1", "payment.succeeded", "pay_1", 500, "succeeded")
	stillTrying.Attempts = []provider.DeliveryAttempt{{At: 1, Status: provider.AttemptStatus{Code: 500}}}
	gaveUp := event("evt_2", "payment.succeeded", "pay_2", 500, "succeeded")
	gaveUp.Attempts = make([]provider.DeliveryAttempt, 6)

	if got := missing([]provider.Event{stillTrying, gaveUp}, map[string]bool{}); len(got) != 2 {
		t.Fatalf("got %d missing events, want 2", len(got))
	}
}

func TestEventIDsAreCollectedInOrder(t *testing.T) {
	ids := eventIDs([]provider.Event{
		event("evt_1", "payment.succeeded", "pay_1", 500, "succeeded"),
		event("evt_2", "payment.refunded", "pay_1", 500, "refunded"),
	})
	if len(ids) != 2 || ids[0] != "evt_1" || ids[1] != "evt_2" {
		t.Fatalf("got %v, want [evt_1 evt_2]", ids)
	}
}

func TestAsIngestEventCarriesTheWholePayment(t *testing.T) {
	// The refund keeps its positive credits figure: turning that into a
	// negative ledger row is ingest's rule, and duplicating it here would be a
	// second home for the same decision.
	in := event("evt_9", "payment.refunded", "pay_9", 500, "refunded")

	out := asIngestEvent(in)

	if out.ID != "evt_9" || out.Type != "payment.refunded" || out.Created != 1767225600 {
		t.Errorf("event header lost in translation: %+v", out)
	}
	if got := out.Data.Payment; got.ID != "pay_9" || got.UserID != "u_1" ||
		got.AmountCents != 500 || got.Credits != 500 || got.Status != "refunded" {
		t.Errorf("payment lost in translation: %+v", got)
	}
}
