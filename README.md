# Credit Wallet Coding Interview

## Overview

You have **25 minutes of build time** (45 minutes in total) to design and implement a small credit wallet service using AI tools.

You may use any AI coding tool (Claude Code / Cursor / Copilot / etc.) and any tech stack (Go / Node.js / Python / Rust / Java / etc.), with any storage you like.

---

## Background

Users buy credits through a third-party payment provider and spend them on features in our product. When a payment succeeds, the provider notifies us with a webhook; that is when the user's credits arrive.

---

## Environment

This project provides a **mock payment provider**. Start it with Docker Compose:

```bash
docker compose up --build
```

No Docker? It's a single Python file with no dependencies:

```bash
python3 services/payment-provider/provider.py
```

| Service | Address | Description |
|---------|---------|-------------|
| payment-provider | `http://localhost:8090` | Takes payments and sends webhooks to `http://localhost:8080/webhooks/payments` |

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

The provider sends a `POST` with a JSON body to `http://localhost:8080/webhooks/payments`:

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
- Submit your code as a Pull Request when you're done
- The interviewer will watch your screen throughout. The focus is on **how you work with AI**, not just the final result

---

## Project Structure

```
.
├── README.md                  ← You are here
├── docker-compose.yml         ← One-command provider startup
├── services/
│   └── payment-provider/      ← Mock payment provider (:8090)
└── wallet/                    ← Your working directory (currently empty)
```
