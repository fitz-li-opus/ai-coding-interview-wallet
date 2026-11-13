# Wallet Service

Flask + SQLite credit wallet. Users buy credits through the payment provider; the provider's
webhook credits the wallet, and credits are spent via the API. Listens on port **8080**.

## Run

```bash
python3 services/payment-provider/provider.py          # terminal 1 (provider on :8090)

cd wallet                                               # terminal 2
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt
python wsgi.py                                          # wallet on :8080, creates wallet.db (instance/)
pytest                                                  # tests
```

Env vars (optional): `DATABASE_URL` (default `sqlite:///wallet.db`), `WEBHOOK_SECRET` (default `whsec_interview_test`).

## API

| Endpoint | Behavior |
|---|---|
| `POST /webhooks/payments` | Provider callback. `payment.succeeded` adds `credits`; `payment.refunded` removes them. Returns `200 {"received": true}`; `400` for a bad signature or payload |
| `GET /wallets/{user_id}` | `{"user_id": "u_1", "balance": 500}` (unknown user: balance 0) |
| `POST /wallets/{user_id}/spend` | Body `{"amount": 100}` -> `{"user_id": "u_1", "balance": 400}`. `409 insufficient_balance` (nothing changes) or `400 invalid_amount` |
| `GET /health` | liveness |

## Demo

```bash
curl -X POST localhost:8090/payments -H 'Content-Type: application/json' \
     -d '{"user_id":"u_1","amount_cents":500}'          # webhook arrives ~1s later
curl localhost:8080/wallets/u_1                          # {"balance":500,...}
curl -X POST localhost:8080/wallets/u_1/spend -H 'Content-Type: application/json' -d '{"amount":100}'
curl -X POST localhost:8090/payments/<payment_id>/refund # refund webhook
curl localhost:8090/events                               # delivery attempts per event
```

## Design

**Tables** (`app/models.py`)
- `wallets(user_id PK, balance, created_at)` — created lazily on the first payment.
- `transactions(id, user_id, type, amount, ref UNIQUE, created_at)` — append-only ledger
  (`deposit` / `refund` / `spend`).

**The provider delivers at-least-once, duplicated and out of order**, so:
- Idempotency: each payment effect has a unique `ref` (`payment:<id>:deposit`, `payment:<id>:refund`).
  Duplicates, retries and "resend" hit the pre-check / unique index and change nothing, yet still get `200`.
- Out of order: a `refunded` first applies the deposit (if missing) and then the refund, so a late
  `succeeded` is a no-op and the final balance is right either way.
- Authenticity: `Opus-Signature: t=…,v1=…` is HMAC-SHA256 over `"<t>.<raw body>"`, compared in constant
  time, with a ±5 min timestamp window (the provider re-signs every attempt). Missing secret fails closed.
- Non-2xx is returned only for real failures (bad signature/payload, unexpected error) so the provider's retries help.

**Concurrency / lost updates** (`app/service.py`)
- Balances are never read-modify-written in Python. Credits/refunds are `UPDATE … SET balance = balance ± x`;
  spend is `UPDATE … SET balance = balance - x WHERE balance >= x` (check and deduct in one statement,
  so no overspend). The ledger row and the balance change commit in one DB transaction.
- Every write path starts with a write statement, so on SQLite concurrent requests queue on the write lock
  (30 s busy timeout, WAL mode) instead of interleaving. If a duplicate slips past the pre-check on another DB,
  the unique `ref` raises `IntegrityError` and the event is re-applied from scratch.
- `tests/test_concurrency.py` hammers a file-backed DB from 16 threads (same wallet, same payment, racing
  succeeded/refunded, spends vs deposits) and asserts exact balances; it fails against a read-modify-write variant.

**Trade-offs**
- A refund of credits that were already spent leaves a **negative balance** (no CHECK constraint); spending
  is blocked until the balance is positive again. The alternative, clamping to 0, would forgive the debt.
- SQLite only (`insert … on conflict do nothing` is SQLite dialect); a single writer is enough for this scope.
- No auth on `/wallets` endpoints — out of scope for the exercise.
