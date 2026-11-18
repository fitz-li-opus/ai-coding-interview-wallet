@refund
Feature: A refund reverses a credit, exactly once

  A refund is the second thing that can happen to one payment, and it is the
  reason this service cannot deduplicate on the payment id. The provider emits
  `payment.succeeded` and `payment.refunded` about the same `data.payment.id`;
  a wallet that remembers payments instead of events swallows the refund and
  keeps money it has already given back.

  Three rules, and each has a scenario that removes it:

    **A reversal is an entry, not an erasure.** The credit stays in the ledger
    and a `reversal` of the same size is appended next to it. Nothing is
    updated in place, so the two events remain visible as two facts, which is
    what `wallet_entries has 2 rows` is asserting.
    **Debt is recorded, not rounded away.** When the credits have already been
    spent there is nothing left to take back, and the balance goes negative.
    The alternative — refusing the refund — would leave the ledger claiming
    money the provider has already returned to the cardholder. A negative
    balance is honest and a wallet in debt simply cannot spend.
    **Apply once per event id.** Delivery is at-least-once, so a redelivered
    refund arrives again and reverses nothing a second time.

  The provider only ever refunds in full — `POST /payments/{id}/refund` takes no
  amount — so a partial reversal is not a shape this system can receive, and the
  reversal always carries the credit's own `credits` value.

  Everything here goes through the real provider: the payment is created with
  its API and the refund is its own refund call, because that is the only way
  the signature on the callback is the provider's and not the harness's.

  Background:
    Given run migration

  Scenario: A refund reverses the credit, and one payment's two events are counted apart
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_1", "amount_cents": 500 }
      """
    And background work has settled
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """
    When payment provider refunds payment "{payment}"
    And background work has settled
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 0;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
      WHERE payment_id = '{payment}' AND kind = 'credit' AND amount = 500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
      WHERE payment_id = '{payment}' AND kind = 'reversal' AND amount = -500;
      """
    And in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM webhook_events
      WHERE payment_id = '{payment}' AND outcome = 'applied';
      """
    And exactly 2 webhook deliveries were accepted

  Scenario: A refund of credits that were already spent leaves the wallet in debt
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_2", "amount_cents": 500 }
      """
    And background work has settled
    And POST /wallets/u_2/spend:
      """json
      { "body": { "amount": 500 } }
      """
    Then response status is 200
    And response body contains:
      """json
      { "user_id": "u_2", "balance": 0 }
      """
    When payment provider refunds payment "{payment}"
    And background work has settled
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_2' AND balance = -500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
      WHERE user_id = 'u_2' AND kind = 'reversal' AND amount = -500;
      """
    And in PostgreSQL query returns 3 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_2';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets w
      WHERE w.user_id = 'u_2'
        AND w.balance = (SELECT COALESCE(SUM(amount), 0)
                           FROM wallet_entries WHERE user_id = 'u_2');
      """

  Scenario: A wallet in debt cannot spend, and the refusal writes nothing
    Given in PostgreSQL:
      """sql
      INSERT INTO wallets (user_id, balance) VALUES ('u_3', -500);
      """
    And in PostgreSQL:
      """sql
      INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id)
      VALUES ('u_3', 'reversal', -500, 'pay_seed_u3', 'evt_seed_u3');
      """
    When POST /wallets/u_3/spend:
      """json
      { "body": { "amount": 100 } }
      """
    Then response status is 422
    And response body contains:
      """json
      { "error": "INSUFFICIENT_BALANCE" }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_3' AND kind = 'spend';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_3' AND balance = -500;
      """

  Scenario: Redelivering both events of a refunded payment reverses nothing twice
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_4", "amount_cents": 500 }
      """
    And background work has settled
    And payment provider refunds payment "{payment}"
    And background work has settled
    And payment provider redelivers each event 3 times
    And background work has settled
    Then in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_4' AND balance = 0;
      """
    And exactly 8 webhook deliveries were accepted

  Scenario: The ledger names the event behind every entry
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_5", "amount_cents": 500 }
      """
    And background work has settled
    And payment provider refunds payment "{payment}"
    And background work has settled
    And GET /wallets/u_5/entries:
      """json
      {}
      """
    Then response status is 200
    And response body contains:
      """json
      {
        "user_id": "u_5",
        "balance": 0,
        "entries": [
          { "kind": "credit", "amount": 500 },
          { "kind": "reversal", "amount": -500 }
        ]
      }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_5' AND event_id IS NULL;
      """
