@observability
Feature: Explaining itself

  A credit system is asked two questions after the fact, usually by a person
  under pressure: *why is this balance what it is*, and *what happened to that
  one request*. Both have to be answerable from outside the process, which is
  what this file is about.

  The ledger answers the first. `GET /wallets/{user_id}/entries` is the balance
  taken apart: one row per movement, in the order they happened, each naming the
  payment and the provider event that caused it. A credit whose row cannot name
  an event id is a credit nobody can trace back to money, so the scenario that
  reads a real provider payment's row asserts that the event id is there.

  A request id answers the second. Every response carries `X-Request-Id`: the
  client's own if it sent one, so a trace that starts in the caller does not
  break here, and a generated one otherwise, so there is no response that cannot
  be found in a log. Errors carry it too — the responses somebody actually goes
  looking for.

  A third rule, unrelated to either: the signing secret never appears in a
  response. It is the only shared secret in the system, and the audit
  endpoint — the one that prints the most about an event — is where a careless
  "include the raw webhook" would put it.

  Background:
    Given run migration

  Scenario: Health is readable without credentials
    When GET /health:
      """json
      {
        "body": {}
      }
      """
    Then response status is 200
    And response body contains:
      """json
      {
        "status": "ok"
      }
      """

  Scenario: A client's own request id is the one that comes back
    When GET /wallets/u_1:
      """json
      {
        "headers": {
          "X-Request-Id": "req-abc-123"
        },
        "body": {}
      }
      """
    Then response status is 200
    And response header "X-Request-Id" contains "req-abc-123"

  Scenario: A response carries a request id even when the client sent none
    # Generated ids are UUIDs, so the hyphen is the part of the shape that is
    # worth asserting from outside.
    When GET /wallets/u_1:
      """json
      {
        "body": {}
      }
      """
    Then response status is 200
    And response header "X-Request-Id" contains "-"

  Scenario: A refused request is traceable too
    Given in PostgreSQL:
      """sql
      INSERT INTO wallets (user_id, balance) VALUES ('u_1', 10);
      INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id) VALUES
        ('u_1', 'credit', 10, 'pay_seed000000001', 'evt_seed000000001');
      """
    When POST /wallets/u_1/spend:
      """json
      {
        "headers": {
          "X-Request-Id": "req-refused-9"
        },
        "body": {
          "amount": 50
        }
      }
      """
    Then response status is 422
    And response header "X-Request-Id" contains "req-refused-9"
    And response body contains:
      """json
      {
        "error": "INSUFFICIENT_BALANCE"
      }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE kind = 'spend';
      """

  Scenario: The ledger is the balance taken apart, in the order it happened
    Given in PostgreSQL:
      """sql
      INSERT INTO wallets (user_id, balance) VALUES ('u_1', 380);
      INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id, created_at) VALUES
        ('u_1', 'credit',   500, 'pay_seed000000001', 'evt_seed000000001', '2026-11-18 09:00:00+00'),
        ('u_1', 'spend',   -100, NULL,                NULL,                '2026-11-18 09:05:00+00'),
        ('u_1', 'reversal', -20, 'pay_seed000000002', 'evt_seed000000002', '2026-11-18 09:10:00+00');
      """
    When GET /wallets/u_1/entries:
      """json
      {
        "body": {}
      }
      """
    Then response status is 200
    And response body contains:
      """json
      {
        "user_id": "u_1",
        "balance": 380,
        "entries": [
          { "kind": "credit",   "amount": 500,  "payment_id": "pay_seed000000001", "event_id": "evt_seed000000001" },
          { "kind": "spend",    "amount": -100, "payment_id": null,                "event_id": null },
          { "kind": "reversal", "amount": -20,  "payment_id": "pay_seed000000002", "event_id": "evt_seed000000002" }
        ]
      }
      """

  Scenario: A credit names the provider event that caused it
    # The audit trail that matters: this row can be carried back to the
    # provider's own event list, which is what makes reconciliation possible at
    # all.
    Given payment provider creates a payment as "payment":
      """json
      {
        "user_id": "u_1",
        "amount_cents": 500
      }
      """
    When background work has settled
    Then exactly 1 webhook delivery was accepted
    When GET /wallets/u_1/entries:
      """json
      {
        "body": {}
      }
      """
    Then response status is 200
    And response body contains:
      """json
      {
        "user_id": "u_1",
        "balance": 500,
        "entries": [
          { "kind": "credit", "amount": 500, "payment_id": "{payment}", "event_id": "<non-null>" }
        ]
      }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
       WHERE user_id = 'u_1' AND kind = 'credit' AND payment_id = '{payment}' AND event_id IS NOT NULL;
      """

  Scenario: No response exposes the signing secret
    # The audit endpoint prints more about an event than anything else here, so
    # it is where "just include the raw webhook" would leak the one secret this
    # system has.
    Given payment provider creates a payment as "payment":
      """json
      {
        "user_id": "u_1",
        "amount_cents": 500
      }
      """
    When background work has settled
    When GET /wallets/u_1/entries:
      """json
      {
        "body": {}
      }
      """
    Then response status is 200
    And response body does not contain "whsec"
    When GET /wallets/u_1:
      """json
      {
        "body": {}
      }
      """
    Then response status is 200
    And response body does not contain "whsec"
    When GET /health:
      """json
      {
        "body": {}
      }
      """
    Then response status is 200
    And response body does not contain "whsec"
