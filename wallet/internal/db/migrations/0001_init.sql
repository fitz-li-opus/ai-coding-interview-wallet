-- Credit wallet schema. This file is the only definition of it; the service
-- embeds it and the BDD harness reads this same path, so there is one copy.
--
-- Three claims are enforced here rather than in application code, because a
-- claim only the application makes holds only while the application is the
-- only writer:
--
--   * a ledger row's sign follows from its kind — a credit is never negative,
--     a spend or a reversal is never positive;
--   * a credit or a reversal always names the event and the payment it came
--     from, and a spend never does: provenance is not optional;
--   * an event is applied at most once, because the provider's event id is the
--     primary key of the inbox and that row is written in the same transaction
--     as the ledger row.
--
-- `wallets.balance` deliberately has NO `>= 0` constraint. A spend may never
-- take a balance below zero, and that rule lives in the conditional UPDATE
-- that performs it. A refund may: the credits really were returned, and a
-- ledger that refuses to say so is a ledger that lies.
-- See plan-wallet-go-postgres.md, decisions D8 and D10.

CREATE TABLE IF NOT EXISTS wallets (
    user_id    text        PRIMARY KEY,
    balance    bigint      NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS wallet_entries (
    id         bigserial   PRIMARY KEY,
    user_id    text        NOT NULL,
    kind       text        NOT NULL CHECK (kind IN ('credit', 'spend', 'reversal')),
    amount     bigint      NOT NULL,
    payment_id text,
    event_id   text,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT wallet_entries_sign_follows_kind CHECK (
        (kind = 'credit'   AND amount > 0) OR
        (kind = 'spend'    AND amount < 0) OR
        (kind = 'reversal' AND amount < 0)
    ),
    CONSTRAINT wallet_entries_provenance CHECK (
        (kind =  'spend' AND payment_id IS NULL     AND event_id IS NULL) OR
        (kind <> 'spend' AND payment_id IS NOT NULL AND event_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS wallet_entries_user_id_id_idx ON wallet_entries (user_id, id);
CREATE INDEX IF NOT EXISTS wallet_entries_event_id_idx   ON wallet_entries (event_id);

-- The inbox: one row per provider event, ever. `deliveries` counts how many
-- times the provider handed us that same event, which is how a duplicate stays
-- visible after it has correctly changed nothing.
CREATE TABLE IF NOT EXISTS webhook_events (
    event_id    text        PRIMARY KEY,
    type        text        NOT NULL,
    payment_id  text,
    status      text,
    outcome     text        NOT NULL CHECK (outcome IN ('applied', 'ignored')),
    deliveries  integer     NOT NULL DEFAULT 1 CHECK (deliveries > 0),
    received_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS webhook_events_payment_id_idx ON webhook_events (payment_id);

-- Spend is the only client-initiated write, so it is the only one that needs a
-- key the client chooses. The stored answer is replayed; a different request
-- under the same key is refused rather than performed.
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key             text        NOT NULL,
    user_id         text        NOT NULL,
    request_hash    text        NOT NULL,
    response_status integer     NOT NULL,
    response_body   jsonb       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (user_id, key)
);
