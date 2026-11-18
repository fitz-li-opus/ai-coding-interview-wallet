package ingest

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wallet/internal/db"
)

// These tests need a real PostgreSQL, because what they are checking is the
// SQL: the ON CONFLICT claim that makes application exactly-once, the CHECK
// constraints that tie a row's sign to its kind, and the UPSERT that creates a
// wallet on arrival. A fake would check my idea of Postgres against itself.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/ingest/
//
// Without that variable they skip: the BDD suite owns the containerised run,
// and this file must not require Docker of anybody running `go test ./...`.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run the SQL tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE wallets, wallet_entries, webhook_events, idempotency_keys`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func succeeded(id, payment, user string, credits int64) Event {
	ev := Event{ID: id, Type: TypeSucceeded, Created: time.Now().Unix()}
	ev.Data.Payment = Payment{ID: payment, UserID: user, AmountCents: credits, Credits: credits, Status: "succeeded"}
	return ev
}

func refunded(id, payment, user string, credits int64) Event {
	ev := Event{ID: id, Type: TypeRefunded, Created: time.Now().Unix()}
	ev.Data.Payment = Payment{ID: payment, UserID: user, AmountCents: credits, Credits: credits, Status: "refunded"}
	return ev
}

func scalar[T any](t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

func TestApplyCreditsAndIsExactlyOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ev := succeeded("evt_a", "pay_a", "u_1", 500)

	// First delivery applies.
	if got, err := Apply(ctx, pool, ev); err != nil || got != OutcomeApplied {
		t.Fatalf("first delivery: got %q, %v", got, err)
	}
	if bal := scalar[int64](t, pool, `SELECT balance FROM wallets WHERE user_id = 'u_1'`); bal != 500 {
		t.Fatalf("balance after credit = %d, want 500", bal)
	}
	if n := scalar[int64](t, pool, `SELECT count(*) FROM wallet_entries WHERE kind = 'credit' AND amount = 500 AND payment_id = 'pay_a' AND event_id = 'evt_a'`); n != 1 {
		t.Fatalf("credit entries = %d, want 1", n)
	}
	if o := scalar[string](t, pool, `SELECT outcome FROM webhook_events WHERE event_id = 'evt_a'`); o != "applied" {
		t.Fatalf("inbox outcome = %q, want applied", o)
	}

	// Three more deliveries of the same event change nothing but the count.
	for i := 0; i < 3; i++ {
		if got, err := Apply(ctx, pool, ev); err != nil || got != OutcomeDuplicate {
			t.Fatalf("redelivery %d: got %q, %v", i, got, err)
		}
	}
	if bal := scalar[int64](t, pool, `SELECT balance FROM wallets WHERE user_id = 'u_1'`); bal != 500 {
		t.Fatalf("balance after redeliveries = %d, want 500", bal)
	}
	if n := scalar[int64](t, pool, `SELECT count(*) FROM wallet_entries`); n != 1 {
		t.Fatalf("entries after redeliveries = %d, want 1", n)
	}
	if n := scalar[int64](t, pool, `SELECT deliveries FROM webhook_events WHERE event_id = 'evt_a'`); n != 4 {
		t.Fatalf("deliveries = %d, want 4", n)
	}
}

// Four copies at one instant: the claim is the serialisation point, so exactly
// one of them may report `applied`.
func TestApplyUnderFourConcurrentCopies(t *testing.T) {
	pool := testPool(t)
	ev := succeeded("evt_race", "pay_race", "u_race", 500)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		outcomes = map[Outcome]int{}
	)
	start := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := Apply(context.Background(), pool, ev)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("apply: %v", err)
				return
			}
			outcomes[got]++
		}()
	}
	close(start)
	wg.Wait()

	if outcomes[OutcomeApplied] != 1 || outcomes[OutcomeDuplicate] != 3 {
		t.Fatalf("outcomes = %v, want 1 applied and 3 duplicate", outcomes)
	}
	if bal := scalar[int64](t, pool, `SELECT balance FROM wallets WHERE user_id = 'u_race'`); bal != 500 {
		t.Fatalf("balance = %d, want 500", bal)
	}
	if n := scalar[int64](t, pool, `SELECT count(*) FROM wallet_entries`); n != 1 {
		t.Fatalf("entries = %d, want 1", n)
	}
}

// Both events of one payment apply, because the key is the event id and not the
// payment id. Deduplicating by payment would swallow the refund.
func TestApplyBothEventsOfOnePayment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := Apply(ctx, pool, succeeded("evt_s", "pay_x", "u_2", 500)); err != nil {
		t.Fatal(err)
	}
	if got, err := Apply(ctx, pool, refunded("evt_r", "pay_x", "u_2", 500)); err != nil || got != OutcomeApplied {
		t.Fatalf("refund: got %q, %v", got, err)
	}
	if bal := scalar[int64](t, pool, `SELECT balance FROM wallets WHERE user_id = 'u_2'`); bal != 0 {
		t.Fatalf("balance = %d, want 0", bal)
	}
	if n := scalar[int64](t, pool, `SELECT count(*) FROM webhook_events WHERE payment_id = 'pay_x'`); n != 2 {
		t.Fatalf("inbox rows = %d, want 2", n)
	}
	if n := scalar[int64](t, pool, `SELECT count(*) FROM wallet_entries WHERE kind = 'reversal' AND amount = -500`); n != 1 {
		t.Fatalf("reversal entries = %d, want 1", n)
	}
}

// A refund that arrives before its credit, and one for a payment that was never
// credited, both take the balance negative. The entries are commutative, so
// nothing is parked and the order does not change where it lands.
func TestApplyRefundFirstGoesNegativeThenNets(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := Apply(ctx, pool, refunded("evt_r1", "pay_o", "u_3", 500)); err != nil {
		t.Fatal(err)
	}
	if bal := scalar[int64](t, pool, `SELECT balance FROM wallets WHERE user_id = 'u_3'`); bal != -500 {
		t.Fatalf("balance after refund-first = %d, want -500", bal)
	}
	if _, err := Apply(ctx, pool, succeeded("evt_c1", "pay_o", "u_3", 500)); err != nil {
		t.Fatal(err)
	}
	if bal := scalar[int64](t, pool, `SELECT balance FROM wallets WHERE user_id = 'u_3'`); bal != 0 {
		t.Fatalf("balance after the credit landed = %d, want 0", bal)
	}
}

// An unknown type is recorded, and applied to nothing: no ledger row, and not
// even a wallet.
func TestApplyUnknownTypeTouchesNothing(t *testing.T) {
	pool := testPool(t)
	ev := Event{ID: "evt_u", Type: "payment.disputed", Created: time.Now().Unix()}
	ev.Data.Payment = Payment{ID: "pay_u", UserID: "u_4", AmountCents: 500, Credits: 500, Status: "succeeded"}

	if got, err := Apply(context.Background(), pool, ev); err != nil || got != OutcomeIgnored {
		t.Fatalf("got %q, %v", got, err)
	}
	if o := scalar[string](t, pool, `SELECT outcome FROM webhook_events WHERE event_id = 'evt_u'`); o != "ignored" {
		t.Fatalf("outcome = %q, want ignored", o)
	}
	if n := scalar[int64](t, pool, `SELECT count(*) FROM wallet_entries`); n != 0 {
		t.Fatalf("entries = %d, want 0", n)
	}
	if n := scalar[int64](t, pool, `SELECT count(*) FROM wallets`); n != 0 {
		t.Fatalf("wallets = %d, want 0 — an ignored event must not conjure one", n)
	}
}

// The invariant the whole design exists for: the balance is the sum of the
// ledger, and every ledger row names an inbox row.
func TestApplyKeepsBalanceEqualToLedgerSum(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		ev := succeeded(fmt.Sprintf("evt_i%d", i), fmt.Sprintf("pay_i%d", i), "u_5", int64(100+i))
		if _, err := Apply(ctx, pool, ev); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Apply(ctx, pool, refunded("evt_i_r", "pay_i0", "u_5", 100)); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int64](t, pool, `
		SELECT count(*) FROM wallets w
		 WHERE w.balance <> (SELECT COALESCE(SUM(e.amount), 0) FROM wallet_entries e WHERE e.user_id = w.user_id)`); n != 0 {
		t.Fatalf("%d wallet(s) disagree with their ledger", n)
	}
	if n := scalar[int64](t, pool, `
		SELECT count(*) FROM wallet_entries e
		 WHERE NOT EXISTS (SELECT 1 FROM webhook_events v WHERE v.event_id = e.event_id)`); n != 0 {
		t.Fatalf("%d ledger row(s) name no inbox row", n)
	}
}
