// Package store is the only place that reads and writes wallet state.
//
// Two rules of this service are enforced by the shape of the SQL in here
// rather than by anything a reviewer has to remember:
//
//   - A spend is ONE conditional UPDATE. The balance is never read, decided
//     upon and then written, so four callers arriving at one instant cannot all
//     pass a check that only the balance can answer. See Spend.
//   - A refusal writes nothing. Every refusal below returns before the
//     transaction commits, so there is no ledger row, no wallet row and no
//     idempotency row to clean up afterwards.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store holds the pool. It is safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store over an open pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Entry is one movement in the ledger. PaymentID and EventID are nil for a
// spend: nothing at the provider caused it.
type Entry struct {
	Kind      string
	Amount    int64
	PaymentID *string
	EventID   *string
}

var (
	// ErrInsufficientBalance means the balance could not pay. Nothing was
	// written — including the wallet row, which a refused spend is not a reason
	// to bring into existence.
	ErrInsufficientBalance = errors.New("store: insufficient balance")

	// ErrIdempotencyKeyReused means the key has already answered a different
	// request. Replaying that answer would answer a question nobody asked.
	ErrIdempotencyKeyReused = errors.New("store: idempotency key reused")
)

// Balance answers for any user id. A user with no row has a balance of zero —
// that is the truth, not a missing record — and asking does not create one.
func (s *Store) Balance(ctx context.Context, userID string) (int64, error) {
	var balance int64
	err := s.pool.QueryRow(ctx, `SELECT balance FROM wallets WHERE user_id = $1`, userID).Scan(&balance)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("read balance: %w", err)
	}
	return balance, nil
}

// Ledger returns the balance and every movement behind it, oldest first: the
// order they were applied in, and the order a running balance makes sense in.
func (s *Store) Ledger(ctx context.Context, userID string) (int64, []Entry, error) {
	balance, err := s.Balance(ctx, userID)
	if err != nil {
		return 0, nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT kind, amount, payment_id, event_id
		  FROM wallet_entries
		 WHERE user_id = $1
		 ORDER BY created_at, id`, userID)
	if err != nil {
		return 0, nil, fmt.Errorf("read ledger: %w", err)
	}
	defer rows.Close()

	entries := make([]Entry, 0, 8)
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Kind, &e.Amount, &e.PaymentID, &e.EventID); err != nil {
			return 0, nil, fmt.Errorf("scan ledger row: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("read ledger: %w", err)
	}
	return balance, entries, nil
}

// SpendParams is one spend request.
type SpendParams struct {
	UserID string
	Amount int64 // strictly positive; the caller has already refused anything else

	// Key is the client's Idempotency-Key, or "" when it sent none.
	Key string
	// RequestHash identifies what the key stands for, so that the same key with
	// a different body can be told apart from a retry.
	RequestHash string

	// Render produces the answer this spend will give. It is called inside the
	// transaction, and its bytes are both what the client receives and what a
	// later retry under the same key replays — a replay is the original answer,
	// not a recomputation of it.
	Render func(balance int64) (status int, body []byte)
}

// SpendOutcome is the answer to give the client.
type SpendOutcome struct {
	Status  int
	Body    []byte
	Balance int64
	// Replayed is true when this answer came from the idempotency record
	// instead of from a deduction that happened now.
	Replayed bool
}

// Spend deducts Amount and answers the new balance.
//
// With a key, it is retry-safe: the same key with the same body replays the
// stored answer and deducts once, the same key with a different body is
// refused, and a key that only ever accompanied a refusal stays free — a key
// burnt by a 4xx would turn one lost response into a credit the caller can
// never spend.
func (s *Store) Spend(ctx context.Context, p SpendParams) (SpendOutcome, error) {
	// Two concurrent requests under one key race to insert the record. The
	// loser sees a unique violation, which is not an error: the winner has
	// already answered, so the loser replays that answer on its second pass.
	for attempt := 0; attempt < 2; attempt++ {
		out, err := s.spendOnce(ctx, p)
		if err != nil && p.Key != "" && isUniqueViolation(err) {
			continue
		}
		return out, err
	}
	return SpendOutcome{}, errors.New("store: idempotency record contended twice")
}

func (s *Store) spendOnce(ctx context.Context, p SpendParams) (SpendOutcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SpendOutcome{}, fmt.Errorf("begin: %w", err)
	}
	// Rollback on every path that does not commit, with a context of its own so
	// that a cancelled request still releases the transaction.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if p.Key != "" {
		var storedHash, storedBody string
		var storedStatus int
		err := tx.QueryRow(ctx, `
			SELECT request_hash, response_status, response_body::text
			  FROM idempotency_keys
			 WHERE user_id = $1 AND key = $2`, p.UserID, p.Key).
			Scan(&storedHash, &storedStatus, &storedBody)
		switch {
		case err == nil && storedHash == p.RequestHash:
			return SpendOutcome{
				Status:   storedStatus,
				Body:     []byte(storedBody),
				Replayed: true,
			}, nil
		case err == nil:
			return SpendOutcome{}, ErrIdempotencyKeyReused
		case !errors.Is(err, pgx.ErrNoRows):
			return SpendOutcome{}, fmt.Errorf("read idempotency key: %w", err)
		}
	}

	// The whole of "never below zero by spending" is this one statement: the row
	// is updated only while the balance still covers the amount, and a wallet
	// that does not exist updates nothing.
	var balance int64
	err = tx.QueryRow(ctx, `
		UPDATE wallets
		   SET balance = balance - $2, updated_at = now()
		 WHERE user_id = $1 AND balance >= $2
		RETURNING balance`, p.UserID, p.Amount).Scan(&balance)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return SpendOutcome{}, ErrInsufficientBalance
	case err != nil:
		return SpendOutcome{}, fmt.Errorf("deduct: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO wallet_entries (user_id, kind, amount)
		VALUES ($1, 'spend', $2)`, p.UserID, -p.Amount); err != nil {
		return SpendOutcome{}, fmt.Errorf("write ledger entry: %w", err)
	}

	status, body := p.Render(balance)

	if p.Key != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO idempotency_keys (key, user_id, request_hash, response_status, response_body)
			VALUES ($1, $2, $3, $4, $5)`,
			p.Key, p.UserID, p.RequestHash, status, string(body)); err != nil {
			return SpendOutcome{}, fmt.Errorf("record idempotency key: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return SpendOutcome{}, fmt.Errorf("commit: %w", err)
	}

	return SpendOutcome{Status: status, Body: body, Balance: balance}, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
