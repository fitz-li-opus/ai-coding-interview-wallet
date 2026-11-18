# Credit Wallet Coding Interview

## Overview

You have **25 minutes of build time** (45 minutes in total) to design and implement a small credit wallet service using AI tools.

You may use any AI coding tool (Claude Code / Cursor / Copilot / etc.) and any tech stack (Go / Node.js / Python / Rust / Java / etc.), with any storage you like.

---

## Background

Users buy credits through a third-party payment provider and spend them on features in our product. When a payment succeeds, the provider notifies us with a webhook; that is when the user's credits arrive.

---

## Environment

This project provides a **mock payment provider**. It's a single Python file with no dependencies (Python 3.8+):

```bash
python3 services/payment-provider/provider.py    # on Windows: python services/payment-provider/provider.py
```

Prefer Docker? `docker compose up --build` works too. The provider then reaches your service through `host.docker.internal`. If webhooks don't arrive, make sure your service listens on `0.0.0.0` rather than `127.0.0.1`.

**On Windows**, run the curl commands in this document from Git Bash or WSL. PowerShell and cmd don't accept the single-quoted JSON bodies, and in Windows PowerShell 5 `curl` is an alias for `Invoke-WebRequest`.

| Service | Address | Description |
|---------|---------|-------------|
| payment-provider | `http://localhost:8090` | Takes payments and sends webhooks to your service on port 8080 at `/webhooks/payments` |

Verify it is running:

```bash
curl http://localhost:8090/health
```

### Payment provider API

| Method & path | Description |
|---------------|-------------|
| `POST /payments` | Body `{"user_id": "u_1", "amount_cents": 500}`. Creates a payment; 1 cent buys 1 credit. A `payment.succeeded` webhook follows about 1 second later. |
| `POST /payments/{id}/refund` | Refunds a payment. A `payment.refunded` webhook follows. |
| `POST /events/{id}/resend` | Redelivers an event, like the "Resend" button in a provider dashboard. |
| `GET /events` | Lists every event and its delivery attempts. |

### Webhooks

The provider sends a `POST` with a JSON body to `/webhooks/payments` on port 8080 (`localhost:8080` when run with `python3`, `host.docker.internal:8080` under Docker):

```json
{
  "id": "evt_8f3a1c2b9d4e5f60",
  "type": "payment.succeeded",
  "created": 1767225600,
  "data": {
    "payment": {
      "id": "pay_1a2b3c4d5e6f7a8b",
      "user_id": "u_1",
      "amount_cents": 500,
      "credits": 500,
      "status": "succeeded"
    }
  }
}
```

Event types: `payment.succeeded`, `payment.refunded`.

- **Delivery is at-least-once.** A response other than 2xx, or no response within 5 seconds, is retried with backoff. The same event may be delivered more than once, and events are not guaranteed to arrive in order.
- **Every request is signed** in the `Opus-Signature` header: `t=<unix timestamp>,v1=<hex signature>`. The signature is `HMAC-SHA256(secret, "<t>.<raw request body>")`. The signing secret is `whsec_interview_test`.

---

## Your Task

Implement a wallet service in the `wallet/` directory, listening on port **8080**:

| Method & path | Behavior |
|---------------|----------|
| `POST /webhooks/payments` | Receives provider webhooks. A successful payment adds its `credits` to the user's wallet. |
| `GET /wallets/{user_id}` | Returns `{"user_id": "u_1", "balance": 500}` |
| `POST /wallets/{user_id}/spend` | Body `{"amount": 100}`. Deducts credits and returns the new balance. If the balance is too low, returns a 4xx and changes nothing. |

This is the **minimum requirement**. Think about what a production-grade payment and credit system needs. Any additional capabilities you design and implement count as bonus points.

---

## Deliverables

- Working, runnable code
- A curl demo: create a payment with the provider, then show the credits in the wallet and spend them
- Be ready to explain your design decisions and trade-offs

---

## Suggested Time Allocation

| Phase | Time | Activity |
|-------|------|----------|
| Understand requirements & design | ~5 min | Read this document, plan your design, align with the interviewer |
| Implement with AI | ~25 min | Build your solution in the `wallet/` directory |
| Demo & checks | ~5 min | curl demo; the interviewer runs a few checks against your service |
| Q&A | ~10 min | Interviewer questions |

---

## Important Notes

- **Do NOT modify** any code under the `services/` directory
- Write all of your code in the `wallet/` directory
- Work in your own fork of this repo. When you're done, open a Pull Request from your fork to this repo's `main` and fill in the PR template
- The interviewer will watch your screen throughout. The focus is on **how you work with AI**, not just the final result

---

## Project Structure

```
.
├── README.md                  ← You are here
├── docker-compose.yml         ← Optional Docker startup for the provider
├── services/
│   └── payment-provider/      ← Mock payment provider (:8090)
└── wallet/                    ← Your working directory (currently empty)
```

---

## Implementation Notes

> Added by the candidate. The implementation lives in [`wallet/`](./wallet) and nothing under
> `services/` was touched — `git diff --stat HEAD -- services/` is empty.
> Full detail: [`wallet/README.md`](./wallet/README.md) · design record: [`plan-wallet-go-postgres.md`](./plan-wallet-go-postgres.md)

**Run it**

```bash
python3 services/payment-provider/provider.py        # provider on :8090
cd wallet && docker compose up --build               # wallet + postgres on :8080
```

**Nine things worth knowing before you probe it.** Each one is a decision, not an accident, and
each is asserted by a scenario in [`wallet/spec/bdd/api`](./wallet/spec/bdd/api).

| | Behaviour | Why it is this way |
|---|---|---|
| 1 | **The webhook signature is verified and mandatory.** A hand-written `curl` to `/webhooks/payments` without a valid `Opus-Signature` gets `400` and writes nothing. To replay a real delivery, use the provider's `POST /events/{id}/resend` — it re-signs for you. | Without verification, anyone who knows the URL can credit themselves |
| 2 | **Duplicates are acknowledged, not refused.** A redelivered event answers `200` and credits nothing a second time. The dedupe key is the **event id**, never the payment id. | A payment emits two events (`succeeded`, `refunded`) that share one payment id; deduping on that id would swallow the refund |
| 3 | **`2xx` means the credits are committed.** Anything else — including `503` when the database is unreachable — is deliberate, so the provider keeps the event and retries. | An ack without a write is the one failure nothing can recover from |
| 4 | **Insufficient balance answers `422 INSUFFICIENT_BALANCE` and writes nothing.** Not "nothing much" — zero ledger rows, balance unchanged, asserted in SQL. | The README's one explicit negative post-condition |
| 5 | **`POST /wallets/{id}/spend` honours an optional `Idempotency-Key`.** Same key + same body replays the first answer byte for byte and spends once; same key + different body is refused `422 IDEMPOTENCY_KEY_REUSED`; a key that only ever accompanied a refusal stays free. | A spend is a client-initiated write over an unreliable network, and the only safe retry is a keyed one |
| 6 | **A balance can be negative.** Refunding credits that were already spent leaves the wallet in debt, and `GET` reports it honestly. Spending is what refuses to go below zero, never the refund. | The credits really were returned; a ledger that refuses to say so is a ledger that lies |
| 7 | **An unknown user answers `200` with `balance: 0` and creates no rows.** | Credits can arrive for a user this service has never seen; a `404` makes every caller write a pointless branch |
| 8 | **An event type this service does not know is recorded, acknowledged, and acted on in no way.** | Answering `4xx` would make the provider redeliver the same event forever |
| 9 | **The three endpoints have no authentication.** Anyone who can reach the service can spend anyone's credits. | Not in scope here, and stated rather than hidden — see Known Gaps in `wallet/README.md` |

**Beyond the minimum**: an append-only ledger (`balance` always equals its sum), an event inbox for
exactly-once crediting, refund handling, a reconciler for events the provider gave up delivering,
`GET /wallets/{user_id}/entries`, `/health`, and `X-Request-Id` on every response.

**Tests**: a machine-checked specification (10 Gherkin feature files, 72 scenarios / 83 runs) under
[`wallet/spec/`](./wallet/spec), run against real containers — PostgreSQL, the service, and this
repository's own unmodified payment provider. See `wallet/README.md` for how to run them.
