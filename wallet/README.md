# Credit Wallet

A credit wallet service: credits arrive from a payment provider's webhook, are read, and are spent.
Go + PostgreSQL. The specification is machine-checked ([RealSpec](#specification-and-tests)), and
every claim below is asserted by a scenario rather than described in prose.

Nothing under `../services/` was modified — `git diff --stat HEAD -- services/` is empty. The
provider the tests run against is that unmodified file, configured only through its environment.

## Run it

```bash
# 1 · the provider the exercise ships, on :8090
python3 ../services/payment-provider/provider.py

# 2 · the wallet and its database, on :8080
docker compose up --build

# 3 · a payment, then the credits, then spend them
curl -s -X POST localhost:8090/payments -d '{"user_id":"u_1","amount_cents":500}'
sleep 2                                      # the provider delivers ~1s after creating a payment
curl -s localhost:8080/wallets/u_1                                  # {"user_id":"u_1","balance":500}
curl -s -X POST localhost:8080/wallets/u_1/spend -d '{"amount":100}' # {"user_id":"u_1","balance":400}
curl -s localhost:8080/wallets/u_1/entries                           # the ledger behind that number
```

Without Docker: `DATABASE_URL=postgres://… go run ./cmd/wallet`.

| Variable | Default | |
|---|---|---|
| `PORT` | `8080` | the service binds `0.0.0.0`, so the provider reaches it from a container |
| `DATABASE_URL` | — | required |
| `WEBHOOK_SECRET` | `whsec_interview_test` | the provider's signing secret |
| `WEBHOOK_TOLERANCE_SECONDS` | `300` | how stale a signature timestamp may be |
| `PROVIDER_BASE_URL` | `http://localhost:8090` | used only by the reconciler |
| `MIGRATE_ON_START` | `true` | applies `internal/db/migrations/0001_init.sql` |

## API

| Method & path | |
|---|---|
| `POST /webhooks/payments` | provider webhooks, signature verified over the raw bytes |
| `GET /wallets/{user_id}` | `{"user_id","balance"}` |
| `POST /wallets/{user_id}/spend` | `{"amount":N}`, optional `Idempotency-Key` header |
| `GET /wallets/{user_id}/entries` | the append-only ledger, oldest first |
| `GET /health` | answers even when the database does not |

Errors are `{"error":"CODE"}` with a stable code: `SIGNATURE_INVALID`,
`SIGNATURE_TIMESTAMP_OUT_OF_RANGE`, `INVALID_PAYLOAD`, `INVALID_AMOUNT`, `INSUFFICIENT_BALANCE`,
`IDEMPOTENCY_KEY_REUSED`, `STORAGE_UNAVAILABLE`. Every response carries `X-Request-Id` — the
client's own if it sent one. The contract is [`spec/openapi/openapi.yaml`](./spec/openapi/openapi.yaml);
the provider's own contract, which this service is a client of, is
[`payment-provider.yaml`](./spec/openapi/payment-provider.yaml).

## Decisions worth knowing

Full record, including the options that were rejected and why, in
[`../plan-wallet-go-postgres.md`](../plan-wallet-go-postgres.md) §2. The nine that change how you
probe the service are in the root [`README.md`](../README.md#implementation-notes). The three that
explain the shape of the code:

**The dedupe key is the event id, not the payment id.** One payment emits two events — `succeeded`
and `refunded` — carrying the *same* payment id. Deduping on it swallows the refund: the money goes
back and the credits never do. `webhook_events` has the provider's event id as its primary key, and
that row is written in the same transaction as the ledger row, which is what makes crediting
exactly-once on top of at-least-once delivery.

**`2xx` is a promise that the credits are committed.** The transaction commits first, the answer is
written after. Everything else — a refusal, a crash, a `503` because the database is unreachable —
leaves the provider holding the event, and it comes again. That is the only recovery path this
endpoint has, and it is why answering `200` without a write is the one failure in this system that
nothing can repair. `ingest.Apply` bounds itself at 4s, under the provider's 5s delivery timeout, so
a hung database produces a refusal the provider *receives* rather than a timeout it waits out.

**A balance may go negative, and only a refund may take it there.** Refunding credits that were
already spent leaves the wallet in debt; reporting `0` instead would be a ledger that lies. So there
is deliberately no `balance >= 0` constraint in the schema — the rule "a spend never overdraws" lives
in the single conditional `UPDATE … WHERE balance >= $amount` that performs it, and a four-way race
scenario holds it down. Ledger entries are commutative because of this: a refund that arrives before
its credit lands on `-500` and then nets to `0`, so event ordering needs no machinery at all.

## Data model

`internal/db/migrations/0001_init.sql` is the only definition; the service embeds it and the test
harness reads the same file. Three rules are enforced by the database rather than by this code,
because a rule only the application knows holds only while the application is the only writer: a
ledger row's sign follows from its kind, a credit or reversal always names the event and payment it
came from (and a spend never does), and an event is applied at most once.

| | |
|---|---|
| `wallets` | `user_id`, `balance` — a cache of the ledger's sum, written in the same transaction |
| `wallet_entries` | append-only: `credit` / `spend` / `reversal`, `balance` always equals its sum |
| `webhook_events` | the inbox, keyed by the provider's event id; `deliveries` counts duplicates |
| `idempotency_keys` | spend only — the one client-initiated write |

## Specification and tests

The specification is `spec/`, and it is enforced rather than described:

```
spec/openapi/*.yaml      the contracts — this service's, and the provider's
spec/bdd/format.yml      the step registry: the only 19 sentences a scenario may use
spec/bdd/api/*.feature   10 files · 72 scenarios · 83 runs
```

A step that is not in the registry is a validation error, not a new step, so a scenario cannot be
quietly reworded to fit the code. Every refusal asserts **in SQL that nothing happened** — the
property a status-code assertion cannot see, and the reason this project is specified this way.

```bash
# the specification is well-formed (free, so it runs first)
npx realspec validate --format spec/bdd/format.yml spec/bdd/api/*.feature

# the scenarios, against real containers
cd test/bdd
BDD_TAGS=@minimum go test ./...                 # the README's minimum requirement, 7 scenarios
BDD_TAGS=@signature go test ./...               # one tag
go test ./...                                   # everything, ~11 min at concurrency 2
BDD_CONCURRENCY=4 go test ./...                 # ~5.5 min
BDD_FORMAT='pretty,cucumber:../../../reports/run.json' go test ./...   # machine-readable
python3 tools/cucumber_html.py ../../../reports/run.json ../../../reports/run.html
```

Each scenario owns three containers — PostgreSQL, this service, and the provider — started and
removed by testcontainers, so no scenario can see another's rows. `DUPLICATE_RATE=0` everywhere:
duplicate deliveries are caused by a step rather than by the provider's dice, because a 30% chance
of a duplicate means "credits once" is untested 70% of the time.

**Result: 83 of 83 runs, 707 of 707 steps, 0 failures** — rendered at
[`../reports/bdd-all.html`](../reports/bdd-all.html). Cost is almost entirely container lifecycle:
~13.5s per scenario of start and teardown against ~8s of assertions for the whole minimum suite.

## Known gaps

- **No authentication on any endpoint.** Anyone who can reach the service can spend anyone's
  credits. The webhook's identity comes from its signature; the other three have none. Out of scope
  here, and stated rather than hidden.
- **"The provider refuses a second refund with `409`" has no scenario.** The step that calls the
  provider expects a `2xx` and cannot carry a counterparty error, so that scenario was deleted
  rather than rewritten into one that passes while asserting nothing. Closing it means adding a
  sentence to the registry, which is a decision about the vocabulary, not about this service.
- **The 5-second webhook budget is not asserted.** The registry has no timing step, and adding one
  turns the suite into a flaky performance test. It is held by `ingest.Apply`'s own 4s bound and a
  unit test instead.
- **Constant-time signature comparison** is covered by unit tests only; a timing assertion that
  passes reliably in CI does not exist.
- **The reconciler is a one-shot command, not a schedule.** `cmd/reconcile` is what the
  `the reconciler runs` step executes inside the container. Production would schedule it, which
  needs a lock and a scenario for two instances running at once.
- **No partial refunds**, because the provider has none: `POST /payments/{id}/refund` takes no amount.

## Layout

```
cmd/wallet · cmd/reconcile       the service, and the one-shot reconciler
internal/api                     HTTP: routing, request id, the wallet endpoints
internal/store                   the wallet reads and the conditional spend
internal/ingest                  signature → inbox → ledger, in one transaction
internal/webhookauth             HMAC verification over raw bytes
internal/provider                a client of the provider's event list
internal/reconcile               what to do about events it gave up delivering
internal/db                      the pool and the schema
spec/                            the specification (see above)
test/bdd/                        the step implementations, its own Go module
```
