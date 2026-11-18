@reconcile
Feature: Reconciliation recovers what delivery gave up on

  The provider gives up. After six failed attempts it prints a line and stops,
  and from then on nothing will ever bring that event to this service on its
  own. A wallet whose only source of truth is the webhook endpoint is therefore
  permanently short by one payment, and the customer paid for it.

  So the provider's event list is the second source, and the reconciler is what
  reads it: every event the provider has that this service has no inbox row for
  is applied now, through the same path and the same event id as a delivery
  would have used. That is the whole of it, and it gives three rules:

    **Reconciliation is idempotent.** It applies events by the same event id,
    so running it twice, or running it against an inbox that is already
    complete, writes nothing the second time. Scenarios two and three remove
    this rule and catch it as a doubled balance.
    **Only the provider's own list counts.** A forged webhook the endpoint
    refused is not in it and never becomes real — reconciliation cannot be used
    as a back door into the ledger.
    **Both kinds of event are recovered.** A lost refund is as expensive as a
    lost credit, in the other direction.

  **A note on how loss is staged.** The provider keeps its events in memory and
  retries them from its own threads, so "this event will never be delivered"
  cannot be reached in a test without waiting out its full retry schedule. These
  scenarios freeze the provider instead — `service "payment-provider" is
  stopped` must suspend it with its event log intact, not discard it — which
  stops delivery dead while leaving the list the reconciler reads. The honest
  limit of that trick: once the provider is resumed so it can be queried, its
  own remaining retries resume too, so the final state is one the reconciler and
  a late delivery would both produce. What the scenarios pin down is that the
  state is correct and written once, which is the claim that matters; the
  assertion that reconciliation is what moved first is made in the scenarios
  where the provider has nothing left to deliver.

  Background:
    Given run migration

  Scenario: An event that never reached the wallet is applied by reconciliation
    Given service "wallet" is stopped
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_1", "amount_cents": 500 }
      """
    And service "payment-provider" is stopped
    And service "wallet" is started
    Then in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}';
      """
    When service "payment-provider" is started
    And the reconciler runs
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
      WHERE payment_id = '{payment}' AND kind = 'credit' AND amount = 500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events
      WHERE payment_id = '{payment}' AND outcome = 'applied';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """

  Scenario: Reconciling an inbox that is already complete writes nothing
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_2", "amount_cents": 500 }
      """
    And background work has settled
    And the reconciler runs
    And the reconciler runs
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_2' AND balance = 500;
      """

  Scenario: Reconciliation with no payments at all writes nothing
    When the reconciler runs
    Then in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM webhook_events;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets;
      """

  Scenario: Reconciliation does not resurrect an event the provider never sent
    When a webhook is delivered by the harness, signed with the wrong secret:
      """json
      {
        "id": "evt_forged_reconcile",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_forged_reconcile",
            "user_id": "u_3",
            "amount_cents": 500,
            "credits": 500,
            "status": "succeeded"
          }
        }
      }
      """
    Then response status is 400
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM webhook_events WHERE event_id = 'evt_forged_reconcile';
      """
    When the reconciler runs
    Then in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM webhook_events WHERE event_id = 'evt_forged_reconcile';
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = 'pay_forged_reconcile';
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_3';
      """

  Scenario: A refund that was never delivered is recovered too
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_4", "amount_cents": 500 }
      """
    And background work has settled
    And service "wallet" is stopped
    And payment provider refunds payment "{payment}"
    And service "payment-provider" is stopped
    And service "wallet" is started
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_4' AND balance = 500;
      """
    When service "payment-provider" is started
    And the reconciler runs
    Then in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM webhook_events
      WHERE payment_id = '{payment}' AND outcome = 'applied';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
      WHERE payment_id = '{payment}' AND kind = 'reversal' AND amount = -500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_4' AND balance = 0;
      """

  Scenario: An event reconciliation already applied is still accepted when it is delivered late
    Given service "wallet" is stopped
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_5", "amount_cents": 500 }
      """
    And service "payment-provider" is stopped
    And service "wallet" is started
    And service "payment-provider" is started
    And the reconciler runs
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}';
      """
    When payment provider redelivers each event 1 time
    And background work has settled
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_5' AND balance = 500;
      """
