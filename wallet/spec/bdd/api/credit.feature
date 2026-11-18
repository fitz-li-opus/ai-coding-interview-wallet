@credit
Feature: Credits arrive from the payment provider

  This is the only way credits come into existence. There is no endpoint that
  mints them, no admin tool that grants them, no field a client can set. A
  wallet grows when, and only when, a signed `payment.succeeded` event is
  accepted here.

  Four rules, and each one has a scenario that removes it:

    **The event names the wallet.** `data.payment.user_id` decides whose
    balance moves, and `data.payment.credits` decides by how much — one cent
    buys one credit, so the number in the event is the number in the wallet.
    Reading the amount from anywhere else is caught by the outline over three
    different amounts.
    **A wallet is not a precondition.** Credits may arrive for a user this
    service has never heard of, so the wallet is created by the arrival. A
    service that requires the wallet to exist first drops that payment, which
    is caught by the scenario where the wallet is created on arrival — and
    which also asserts that exactly one wallet now exists, so nothing else was
    conjured along the way.
    **The balance is derived, never invented.** Every movement writes an
    append-only `wallet_entries` row, and `wallets.balance` must always equal
    their sum. A balance updated without a ledger row, or a ledger row without
    the balance, is caught by the invariant scenario.
    **Acknowledging means it is credited.** The provider retries anything that
    is not 2xx, so a 2xx is a promise that the transaction committed. The
    delivery record on the provider's side — one accepted delivery, read from
    the provider and not from our own tables — is what proves the promise was
    made exactly once. The failure direction of this rule (crash before commit,
    ack never sent, event comes again) lives in `resilience.feature`.

  Everything on the happy path here is driven by the real payment provider:
  it creates the payment, it signs the event, it delivers it. The harness signs
  nothing, so "the signature was right" is never asserted by computing a digest
  in a feature file — it is asserted by the wallet accepting what the provider
  sent. The one harness-delivered event in this file carries a type the
  provider will never emit, which is exactly why the harness has to be the one
  to send it.

  Background:
    Given run migration

  @minimum
  Scenario: A succeeded payment credits the wallet named in the event
    Given payment provider creates a payment as "payment":
      """json
      { "user_id": "u_1", "amount_cents": 500 }
      """
    When background work has settled
    When GET /wallets/u_1:
      """json
      {}
      """
    Then response status is 200
    And response body contains:
      """json
      { "user_id": "u_1", "balance": 500 }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
       WHERE user_id = 'u_1' AND kind = 'credit' AND amount = 500
         AND payment_id = '{payment}' AND event_id IS NOT NULL;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events
       WHERE payment_id = '{payment}' AND type = 'payment.succeeded'
         AND status = 'succeeded' AND outcome = 'applied';
      """
    And exactly 1 webhook delivery was accepted
    And exactly 0 webhook deliveries were refused

  Scenario Outline: One cent buys one credit, whatever the amount
    Given payment provider creates a payment as "payment":
      """json
      { "user_id": "u_1", "amount_cents": "<amount>" }
      """
    When background work has settled
    When GET /wallets/u_1:
      """json
      {}
      """
    Then response status is 200
    And response body contains:
      """json
      { "user_id": "u_1", "balance": "<amount>" }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets w
       WHERE w.user_id = 'u_1'
         AND w.balance = (SELECT COALESCE(SUM(e.amount), 0) FROM wallet_entries e WHERE e.user_id = w.user_id);
      """

    Examples:
      | amount |
      | 1      |
      | 500    |
      | 2500   |

  Scenario: Two payments for one user accumulate
    Given payment provider creates a payment as "firstPayment":
      """json
      { "user_id": "u_2", "amount_cents": 300 }
      """
    And payment provider creates a payment as "secondPayment":
      """json
      { "user_id": "u_2", "amount_cents": 450 }
      """
    When background work has settled
    When GET /wallets/u_2:
      """json
      {}
      """
    Then response status is 200
    And response body contains:
      """json
      { "user_id": "u_2", "balance": 750 }
      """
    And in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_2' AND kind = 'credit';
      """
    And in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM webhook_events WHERE outcome = 'applied';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{firstPayment}' AND amount = 300;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{secondPayment}' AND amount = 450;
      """
    And exactly 2 webhook deliveries were accepted

  Scenario: One user's credits never reach another user's wallet
    Given payment provider creates a payment as "payment":
      """json
      { "user_id": "u_3", "amount_cents": 500 }
      """
    And payment provider creates a payment as "otherPayment":
      """json
      { "user_id": "u_4", "amount_cents": 125 }
      """
    When background work has settled
    When GET /wallets/u_3:
      """json
      {}
      """
    Then response status is 200
    And response body contains:
      """json
      { "user_id": "u_3", "balance": 500 }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_4' AND balance = 125;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_3' AND payment_id = '{otherPayment}';
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_4' AND payment_id = '{payment}';
      """

  @minimum
  Scenario: A payment for a user this service has never seen creates the wallet
    Given payment provider creates a payment as "payment":
      """json
      { "user_id": "u_5", "amount_cents": 700 }
      """
    When background work has settled
    When GET /wallets/u_5:
      """json
      {}
      """
    Then response status is 200
    And response body contains:
      """json
      { "user_id": "u_5", "balance": 700 }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_5' AND payment_id = '{payment}';
      """

  Scenario: Credits add to a balance that was already there
    Given in PostgreSQL:
      """sql
      INSERT INTO wallets (user_id, balance) VALUES ('u_8', 200);
      """
    And in PostgreSQL:
      """sql
      INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id) VALUES
        ('u_8', 'credit', 200, 'pay_0000000000000001', 'evt_0000000000000001');
      """
    And payment provider creates a payment as "payment":
      """json
      { "user_id": "u_8", "amount_cents": 300 }
      """
    When background work has settled
    When GET /wallets/u_8:
      """json
      {}
      """
    Then response status is 200
    And response body contains:
      """json
      { "user_id": "u_8", "balance": 500 }
      """
    And in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_8';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets w
       WHERE w.user_id = 'u_8'
         AND w.balance = (SELECT COALESCE(SUM(e.amount), 0) FROM wallet_entries e WHERE e.user_id = w.user_id);
      """

  Scenario: The balance is the sum of the ledger, not a number of its own
    Given payment provider creates a payment as "firstPayment":
      """json
      { "user_id": "u_7", "amount_cents": 500 }
      """
    And payment provider creates a payment as "secondPayment":
      """json
      { "user_id": "u_7", "amount_cents": 300 }
      """
    When background work has settled
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets w
       WHERE w.user_id = 'u_7' AND w.balance = 800
         AND w.balance = (SELECT COALESCE(SUM(e.amount), 0) FROM wallet_entries e WHERE e.user_id = w.user_id);
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE event_id IS NULL;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries e
       WHERE NOT EXISTS (SELECT 1 FROM webhook_events v WHERE v.event_id = e.event_id);
      """

  Scenario: An event type this service does not know is recorded and acted on in no way
    When a webhook is delivered by the harness:
      """json
      {
        "id": "evt_7d2c4b1a9e8f3056",
        "type": "payment.disputed",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_3f1e2d4c5b6a7980",
            "user_id": "u_6",
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
      SELECT 1 FROM webhook_events
       WHERE event_id = 'evt_7d2c4b1a9e8f3056' AND type = 'payment.disputed' AND outcome = 'ignored';
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets;
      """
