@dedupe
Feature: At-least-once delivery, applied exactly once

  The provider's contract says delivery is at-least-once: anything that is not
  2xx within five seconds is retried with backoff, and the same event is
  sometimes delivered again for no reason at all. So the same credit arrives
  twice, or four times at one instant, and the balance must move once.

  The rule that makes this work is the choice of key, and it is the one thing
  in this file worth staring at: **the key is the event id, not the payment
  id.** `webhook_events` has the provider's `id` as its primary key, inserted
  in the same transaction as the ledger row, so the second copy of an event
  loses a race against a unique index rather than against a flag somebody
  remembered to check.

  Keying on the payment id would also make every scenario here pass — and it
  would silently eat the refund, because `payment.succeeded` and
  `payment.refunded` are two events carrying the same `data.payment.id`. The
  scenario that both events of one payment apply is the one that catches it,
  and it is the reason this file exists rather than being three lines inside
  `credit.feature`.

  The second rule: **a duplicate is acknowledged, not refused.** A 4xx or 5xx
  on a copy we have already applied tells the provider to try again, and it
  will, until it gives up — turning a harmless duplicate into six deliveries
  and, eventually, a lost event. The scenario asserting zero refused deliveries
  is what removes that.

  Duplicates here are produced deterministically, through the provider's own
  resend — the dashboard button, which is also how a human recovers a dropped
  event. The provider's random duplication is switched off in the default stack
  (`DUPLICATE_RATE=0`) so that a passing suite means the wallet deduplicates,
  not that a coin came up tails; one scenario turns it on at
  `DUPLICATE_RATE=1` to prove the wallet survives the spontaneous kind too.

  Background:
    Given run migration

  Scenario: An event delivered again three times credits once
    Given payment provider creates a payment as "payment":
      """json
      { "user_id": "u_1", "amount_cents": 500 }
      """
    And background work has settled
    When payment provider redelivers each event 3 times
    And background work has settled
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}' AND kind = 'credit' AND amount = 500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}' AND outcome = 'applied';
      """
    And exactly 4 webhook deliveries were accepted

  Scenario Outline: Four copies of one event at one instant credit once
    Given payment provider creates a payment as "payment":
      """json
      { "user_id": "u_1", "amount_cents": 500 }
      """
    And background work has settled
    When these things happen at one instant:
      """json
      [
        { "step": "payment provider redelivers each event 1 time" },
        { "step": "payment provider redelivers each event 1 time" },
        { "step": "payment provider redelivers each event 1 time" },
        { "step": "payment provider redelivers each event 1 time" }
      ]
      """
    And background work has settled
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}';
      """
    And exactly 5 webhook deliveries were accepted
    And exactly 0 webhook deliveries were refused

    Examples:
      | run |
      | 1   |
      | 2   |
      | 3   |

  Scenario: A copy we have already applied is acknowledged, not refused
    Given payment provider creates a payment as "payment":
      """json
      { "user_id": "u_1", "amount_cents": 500 }
      """
    And background work has settled
    When payment provider redelivers each event 1 time
    And background work has settled
    Then exactly 2 webhook deliveries were accepted
    And exactly 0 webhook deliveries were refused
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """

  @stack:duplicating
  Scenario: The provider's own spontaneous duplicate credits once
    Given payment provider creates a payment as "payment":
      """json
      { "user_id": "u_1", "amount_cents": 500 }
      """
    When background work has settled
    Then exactly 2 webhook deliveries were accepted
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """

  Scenario: Both events of one payment apply, because the key is the event
    Given payment provider creates a payment as "payment":
      """json
      { "user_id": "u_1", "amount_cents": 500 }
      """
    And background work has settled
    When payment provider refunds payment "{payment}"
    And background work has settled
    Then in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}' AND type = 'payment.succeeded';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}' AND type = 'payment.refunded';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}' AND kind = 'credit' AND amount = 500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}' AND kind = 'reversal' AND amount = -500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 0;
      """
    And exactly 2 webhook deliveries were accepted

  Scenario Outline: Every event of a payment, redelivered at one instant, applies once
    Given payment provider creates a payment as "payment":
      """json
      { "user_id": "u_1", "amount_cents": 500 }
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
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 0;
      """
    And exactly 6 webhook deliveries were accepted
    And exactly 0 webhook deliveries were refused

    Examples:
      | run |
      | 1   |
      | 2   |
      | 3   |
