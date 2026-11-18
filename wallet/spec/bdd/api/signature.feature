@signature
Feature: The signature is the only reason to believe a webhook

  `POST /webhooks/payments` is a public endpoint that adds money. Nothing else
  about the request is evidence: not the source address, not the shape of the
  JSON, not the `pay_` prefix on the payment id. The `Opus-Signature` header is
  the whole of the authentication, and it is checked before anything is read
  from the body.

  What is verified, and the rule each scenario removes:

    **The bytes that arrived.** The signature covers `"<t>." + raw request
    body`, so verification happens against the bytes received, never against a
    re-serialised copy of the parsed object. A service that re-encodes first
    rejects everything the moment the provider's key order or spacing differs
    from its own — and the control scenario, the same payload correctly signed,
    is what keeps all five refusals below from passing for that reason instead
    of the right one.
    **Who signed it.** A body signed with some other secret is refused, which
    is the difference between a signature check and a signature-shaped
    decoration.
    **What was signed.** `tampered after signing` edits the amount after the
    digest was computed — the attack this header exists to stop.
    **When it was signed.** `t` is the delivery time, re-stamped on every
    attempt, so a captured request stays replayable forever unless a tolerance
    window is enforced. The window is `WEBHOOK_TOLERANCE_SECONDS` (300 by
    default), which is comfortably wider than the provider's whole retry
    sequence of 1, 2, 4, 8 and 16 seconds.
    **That it parses at all.** A valid signature over a body that is not JSON
    is still not an event.

  Every refusal asserts the same thing twice: the status, and that **nothing
  happened** — no ledger row, no inbox row, not even a wallet. A service that
  refuses the forgery after crediting the wallet would pass a status-code-only
  test, and that is the single reason these scenarios are written in this
  vocabulary.

  These are the only webhooks in the suite the harness constructs, and that is
  deliberate: the real provider cannot be asked to forge its own signature,
  back-date it, or truncate its own body. Everything the provider *can* do is
  driven through the provider. The harness never computes a digest the wallet
  is supposed to accept except in the two control scenarios, and no digest
  appears anywhere in this file.

  One consequence worth stating: a refusal here is a 4xx, and a 4xx is what
  tells the real provider to retry. That is correct — a forgery does not come
  from the provider, so there is no delivery to retry, and the one event the
  provider does hold is unaffected, which the last scenario asserts by letting
  the genuine copy through afterwards.

  Background:
    Given run migration

  Scenario: A webhook nobody signed changes nothing
    When a webhook is delivered by the harness, unsigned:
      """json
      {
        "id": "evt_5c1f9a2b7d4e6038",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_9a1b2c3d4e5f6071",
            "user_id": "u_1",
            "amount_cents": 500,
            "credits": 500,
            "status": "succeeded"
          }
        }
      }
      """
    Then response status is 400
    And response body contains:
      """json
      { "error": "SIGNATURE_INVALID" }
      """
    And response body does not contain "whsec"
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM webhook_events;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets;
      """

  Scenario: A webhook signed with another secret changes nothing
    When a webhook is delivered by the harness, signed with the wrong secret:
      """json
      {
        "id": "evt_5c1f9a2b7d4e6038",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_9a1b2c3d4e5f6071",
            "user_id": "u_1",
            "amount_cents": 500,
            "credits": 500,
            "status": "succeeded"
          }
        }
      }
      """
    Then response status is 400
    And response body contains:
      """json
      { "error": "SIGNATURE_INVALID" }
      """
    And response body does not contain "whsec"
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM webhook_events;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets;
      """

  Scenario: An amount edited after the body was signed changes nothing
    When a webhook is delivered by the harness, tampered after signing:
      """json
      {
        "id": "evt_5c1f9a2b7d4e6038",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_9a1b2c3d4e5f6071",
            "user_id": "u_1",
            "amount_cents": 500,
            "credits": 500,
            "status": "succeeded"
          }
        }
      }
      """
    Then response status is 400
    And response body contains:
      """json
      { "error": "SIGNATURE_INVALID" }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM webhook_events;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets;
      """

  Scenario: A signature header this service cannot parse changes nothing
    When a webhook is delivered by the harness, with a malformed signature header:
      """json
      {
        "id": "evt_5c1f9a2b7d4e6038",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_9a1b2c3d4e5f6071",
            "user_id": "u_1",
            "amount_cents": 500,
            "credits": 500,
            "status": "succeeded"
          }
        }
      }
      """
    Then response status is 400
    And response body contains:
      """json
      { "error": "SIGNATURE_INVALID" }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM webhook_events;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets;
      """

  Scenario: A correct signature from outside the tolerance window changes nothing
    When a webhook is delivered by the harness, with a stale timestamp:
      """json
      {
        "id": "evt_5c1f9a2b7d4e6038",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_9a1b2c3d4e5f6071",
            "user_id": "u_1",
            "amount_cents": 500,
            "credits": 500,
            "status": "succeeded"
          }
        }
      }
      """
    Then response status is 400
    And response body contains:
      """json
      { "error": "SIGNATURE_TIMESTAMP_OUT_OF_RANGE" }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM webhook_events;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets;
      """

  Scenario: A signed body that is not JSON changes nothing
    When a webhook is delivered by the harness, with a malformed body:
      """json
      {
        "id": "evt_5c1f9a2b7d4e6038",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_9a1b2c3d4e5f6071",
            "user_id": "u_1",
            "amount_cents": 500,
            "credits": 500,
            "status": "succeeded"
          }
        }
      }
      """
    Then response status is 400
    And response body contains:
      """json
      { "error": "<non-null>" }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM webhook_events;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets;
      """

  Scenario: The same payload, correctly signed, credits the wallet
    When a webhook is delivered by the harness:
      """json
      {
        "id": "evt_5c1f9a2b7d4e6038",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_9a1b2c3d4e5f6071",
            "user_id": "u_1",
            "amount_cents": 500,
            "credits": 500,
            "status": "succeeded"
          }
        }
      }
      """
    Then response status is 200
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events WHERE event_id = 'evt_5c1f9a2b7d4e6038' AND outcome = 'applied';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
       WHERE event_id = 'evt_5c1f9a2b7d4e6038' AND kind = 'credit' AND amount = 500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """

  Scenario: A refused forgery does not consume the event id it claimed
    When a webhook is delivered by the harness, tampered after signing:
      """json
      {
        "id": "evt_5c1f9a2b7d4e6038",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_9a1b2c3d4e5f6071",
            "user_id": "u_1",
            "amount_cents": 500,
            "credits": 500,
            "status": "succeeded"
          }
        }
      }
      """
    And a webhook is delivered by the harness:
      """json
      {
        "id": "evt_5c1f9a2b7d4e6038",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_9a1b2c3d4e5f6071",
            "user_id": "u_1",
            "amount_cents": 500,
            "credits": 500,
            "status": "succeeded"
          }
        }
      }
      """
    Then response status is 200
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events WHERE event_id = 'evt_5c1f9a2b7d4e6038' AND outcome = 'applied';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE event_id = 'evt_5c1f9a2b7d4e6038';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """
