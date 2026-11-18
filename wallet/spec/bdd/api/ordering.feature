@ordering
Feature: Events arrive in any order and the ledger does not care

  The provider says events are not guaranteed to arrive in order, and its
  implementation shows why: each event is emitted on its own thread with its own
  delay and its own retry schedule, so a refund can overtake the credit it
  reverses. Nothing in the transport will fix this, and no amount of care in the
  handler can make an event that has not arrived yet available.

  The rule this file defends is that **entries are commutative**. Every event
  becomes one signed entry against one payment — a credit is positive, a
  reversal is negative — and addition does not care which addend lands first.
  An early refund drives the balance below zero and the late credit brings it
  back to where it belongs.

  The alternative was considered and rejected: park the refund until its credit
  shows up. That needs a dependency table, a worker to drain it, a timeout
  policy, and an answer for a credit that never arrives — and it ends at the
  same number. The cost of commutativity is one honest negative balance in the
  window between the two events, which the scenarios below assert rather than
  hide. The one thing it must not do is let a negative balance be *spent*, so
  that is asserted here too.

  **Why the harness delivers these, not the provider.** Deliberate disorder is
  the one thing the real provider cannot be asked for: its two threads are
  started by its own code and nothing exposed by its API controls their order.
  So the ordering scenarios are driven by harness-delivered webhooks, correctly
  signed and shaped exactly like the provider's own — the only deviation is who
  decided the order. The race scenario at the end goes back through the real
  provider, because there the question is about simultaneity rather than order.

  Background:
    Given run migration

  Scenario: A refund that arrives before its credit nets to zero when the credit lands
    When a webhook is delivered by the harness:
      """json
      {
        "id": "evt_order_refund_first",
        "type": "payment.refunded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_order_1",
            "user_id": "u_1",
            "amount_cents": 500,
            "credits": 500,
            "status": "refunded"
          }
        }
      }
      """
    Then response status is 200
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = -500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
      WHERE payment_id = 'pay_order_1' AND kind = 'reversal' AND amount = -500;
      """
    When a webhook is delivered by the harness:
      """json
      {
        "id": "evt_order_credit_second",
        "type": "payment.succeeded",
        "created": 1767225601,
        "data": {
          "payment": {
            "id": "pay_order_1",
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
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 0;
      """
    And in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = 'pay_order_1';
      """
    And in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM webhook_events
      WHERE payment_id = 'pay_order_1' AND outcome = 'applied';
      """

  Scenario Outline: The order the two events arrive in does not change the balance
    When a webhook is delivered by the harness:
      """json
      {
        "id": "evt_commute_first",
        "type": "<firstType>",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_commute",
            "user_id": "u_6",
            "amount_cents": 700,
            "credits": 700,
            "status": "<firstStatus>"
          }
        }
      }
      """
    And a webhook is delivered by the harness:
      """json
      {
        "id": "evt_commute_second",
        "type": "<secondType>",
        "created": 1767225601,
        "data": {
          "payment": {
            "id": "pay_commute",
            "user_id": "u_6",
            "amount_cents": 700,
            "credits": 700,
            "status": "<secondStatus>"
          }
        }
      }
      """
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_6' AND balance = 0;
      """
    And in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = 'pay_commute';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
      WHERE payment_id = 'pay_commute' AND kind = 'credit' AND amount = 700;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
      WHERE payment_id = 'pay_commute' AND kind = 'reversal' AND amount = -700;
      """

    Examples:
      | firstType         | firstStatus | secondType        | secondStatus |
      | payment.succeeded | succeeded   | payment.refunded  | refunded     |
      | payment.refunded  | refunded    | payment.succeeded | succeeded    |

  Scenario: A wallet whose only event is a refund cannot spend
    When a webhook is delivered by the harness:
      """json
      {
        "id": "evt_order_debt",
        "type": "payment.refunded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_order_debt",
            "user_id": "u_7",
            "amount_cents": 500,
            "credits": 500,
            "status": "refunded"
          }
        }
      }
      """
    Then response status is 200
    When POST /wallets/u_7/spend:
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
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_7' AND kind = 'spend';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_7' AND balance = -500;
      """

  Scenario: A refund for a payment this service never credited is still recorded
    When a webhook is delivered by the harness:
      """json
      {
        "id": "evt_order_orphan",
        "type": "payment.refunded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_never_seen",
            "user_id": "u_9",
            "amount_cents": 300,
            "credits": 300,
            "status": "refunded"
          }
        }
      }
      """
    Then response status is 200
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_9' AND balance = -300;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
      WHERE payment_id = 'pay_never_seen' AND kind = 'reversal' AND amount = -300;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events
      WHERE event_id = 'evt_order_orphan' AND outcome = 'applied';
      """

  Scenario Outline: Two copies of each event of one payment at one instant apply once
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_race_<run>", "amount_cents": 500 }
      """
    And background work has settled
    And payment provider refunds payment "{payment}"
    And background work has settled
    When these things happen at one instant:
      """json
      [
        { "step": "payment provider redelivers each event 1 time" },
        { "step": "payment provider redelivers each event 1 time" }
      ]
      """
    Then background work has settled
    And exactly 6 webhook deliveries were accepted
    And in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_race_<run>' AND balance = 0;
      """

    Examples:
      | run |
      | 1   |
      | 2   |
      | 3   |
