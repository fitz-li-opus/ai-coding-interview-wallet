@resilience
Feature: A refusal is a retry, never a loss

  At-least-once delivery is not a hazard to be tolerated here, it is the entire
  recovery mechanism. The provider retries any delivery that does not answer
  2xx within five seconds, six times, with backoff. Everything this service does
  about failure comes down to one rule:

    **Acknowledge after committing.** A 2xx means the credit is in the ledger and
    will still be there after a restart. Anything else — a crash, a database
    that is gone, a process that was not running — must leave the provider
    holding the event, because the provider trying again is the only thing that
    will ever bring it back.

  Remove that rule and two of the scenarios below fail in opposite directions.
  Acknowledge too early and "nothing was written while the database was gone"
  becomes a wallet that is permanently short. Acknowledge too late — after the
  commit, but counting a late ack as a failure — and the retry arrives to find
  the credit already applied, which is the last scenario and the reason the
  event id is the inbox's primary key rather than a log line.

  **How the retries are made deterministic.** Two of the delivery paths here are
  the provider's own and their timing is not ours: the automatic sequence fires
  at roughly one second and retries at one, two, four, eight and sixteen second
  intervals, so the number of attempts that land inside an outage depends on how
  long the outage lasts. Where an exact count matters, the delivery is driven
  through the provider's resend endpoint instead, which makes exactly one
  attempt and never retries it. Nothing in this file waits for a fixed number of
  seconds; the only wait is for the work to have settled.

  Background:
    Given run migration

  Scenario: A payment that arrives while the wallet is down credits exactly once when it returns
    Given service "wallet" is stopped
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_1", "amount_cents": 500 }
      """
    And service "wallet" is started
    And background work has settled
    Then in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
      WHERE payment_id = '{payment}' AND kind = 'credit' AND amount = 500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM webhook_events
      WHERE payment_id = '{payment}' AND outcome = 'applied';
      """
    And exactly 1 webhook delivery was accepted

  Scenario: A delivery the wallet could not answer is retried and then credits once
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_2", "amount_cents": 500 }
      """
    And background work has settled
    Then exactly 1 webhook delivery was accepted
    When service "wallet" is stopped
    And payment provider redelivers each event 1 time
    Then exactly 1 webhook delivery was refused
    When service "wallet" is started
    And payment provider redelivers each event 1 time
    And background work has settled
    Then exactly 2 webhook deliveries were accepted
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_2' AND balance = 500;
      """

  Scenario: Nothing is written while the database is gone, and the event is not lost
    Given service "postgres" is stopped
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_3", "amount_cents": 500 }
      """
    And service "payment-provider" is stopped
    And service "postgres" is started
    Then in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM webhook_events WHERE payment_id = '{payment}';
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE payment_id = '{payment}';
      """
    When service "payment-provider" is started
    And background work has settled
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
      SELECT 1 FROM wallets WHERE user_id = 'u_3' AND balance = 500;
      """

  Scenario: A webhook that cannot be stored is refused rather than acknowledged
    # The scenario above proves the event survives. This one proves the sentence
    # the service has to say for that to happen. An acknowledgement is a promise
    # that the credits are recorded, so a service that cannot reach its database
    # has exactly one honest answer, and it is not 2xx. Answer 200 here and the
    # provider crosses the event off its list, which is how credits are lost for
    # good — the one failure in this whole system that nothing can recover from.
    Given service "postgres" is stopped
    When a webhook is delivered by the harness:
      """json
      {
        "id": "evt_4b8e1d3f6a2c9057",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_4b8e1d3f6a2c9057",
            "user_id": "u_7",
            "amount_cents": 500,
            "credits": 500,
            "status": "succeeded"
          }
        }
      }
      """
    Then response status is 503
    And response body contains:
      """json
      { "error": "STORAGE_UNAVAILABLE" }
      """
    When service "postgres" is started
    And a webhook is delivered by the harness:
      """json
      {
        "id": "evt_4b8e1d3f6a2c9057",
        "type": "payment.succeeded",
        "created": 1767225600,
        "data": {
          "payment": {
            "id": "pay_4b8e1d3f6a2c9057",
            "user_id": "u_7",
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
      SELECT 1 FROM wallet_entries
      WHERE event_id = 'evt_4b8e1d3f6a2c9057' AND kind = 'credit' AND amount = 500;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_7' AND balance = 500;
      """

  Scenario: A balance that was acknowledged survives a restart of the wallet
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_4", "amount_cents": 900 }
      """
    And background work has settled
    And POST /wallets/u_4/spend:
      """json
      { "body": { "amount": 400 } }
      """
    Then response status is 200
    When service "wallet" is stopped
    And service "wallet" is started
    And GET /wallets/u_4:
      """json
      {}
      """
    Then response status is 200
    And response body contains:
      """json
      { "user_id": "u_4", "balance": 500 }
      """
    And in PostgreSQL query returns 2 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_4';
      """

  Scenario: A delivery that arrives after the ack was lost credits nothing a second time
    When payment provider creates a payment as "payment":
      """json
      { "user_id": "u_5", "amount_cents": 500 }
      """
    And background work has settled
    And payment provider redelivers each event 3 times
    And background work has settled
    Then exactly 4 webhook deliveries were accepted
    And in PostgreSQL query returns 1 row:
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
