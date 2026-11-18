@spend
Feature: Spending credits

  This is the only endpoint a user's own client calls, and the only one that can
  take credits away because somebody asked it to. The requirement the brief
  states for it is unusually precise: when the balance is too low it returns a
  4xx **and changes nothing**. That second half is the whole reason this file
  exists in this form. A test that reads the status code alone passes for a
  service that deducts the credits and then refuses, or that writes a
  half-finished ledger row and then refuses, so every refusal here is followed
  by an assertion that the ledger did not grow and the balance did not move.

  Four rules, each with a scenario that removes it:

    **Never below zero by spending.** A spend is one conditional write — the row
    is updated only while the balance still covers the amount — so the check and
    the deduction cannot be separated by anything, including three other callers.
    The racing scenario is what catches a read-then-write: four callers, each
    asking for 60 against a balance of 100, where a service that reads first
    lets two of them through and ends at -20.
    **A refusal is not a deduction.** 422 and `INSUFFICIENT_BALANCE`, no new
    ledger row, the same balance afterwards.
    **An amount is a positive integer.** Zero, negative, fractional, textual and
    absent are all refused as `INVALID_AMOUNT` before anything is written, so a
    malformed request cannot move a balance by a fraction of a credit or by a
    cast.
    **A retry is not a second spend.** The network this endpoint is called over
    drops answers, and the only safe retry is a keyed one: the same
    `Idempotency-Key` replays the answer it gave the first time and deducts
    once. The key is optional, because the brief does not ask for it; what it is
    not is advisory.

  A negative balance — which only a refund can produce, see refund.feature —
  refuses every spend until credits arrive again. Nothing special happens: the
  balance does not cover the amount, which is the same rule as everywhere else.

  Background:
    Given run migration
    And in PostgreSQL:
      """sql
      INSERT INTO wallets (user_id, balance) VALUES ('u_1', 500);
      INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id) VALUES
        ('u_1', 'credit', 500, 'pay_seed000000001', 'evt_seed000000001');
      """

  @minimum
  Scenario: Spending deducts the credits and answers with the new balance
    When POST /wallets/u_1/spend:
      """json
      {
        "body": {
          "amount": 100
        }
      }
      """
    Then response status is 200
    And response body contains:
      """json
      {
        "user_id": "u_1",
        "balance": 400
      }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 400;
      """
    # One entry, and it is negative: the ledger records a movement, not a state.
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
       WHERE user_id = 'u_1' AND kind = 'spend' AND amount = -100
      HAVING count(*) = 1;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_1' HAVING count(*) = 2;
      """

  @minimum
  Scenario: Spending the entire balance is allowed and lands exactly on zero
    When POST /wallets/u_1/spend:
      """json
      {
        "body": {
          "amount": 500
        }
      }
      """
    Then response status is 200
    And response body contains:
      """json
      {
        "user_id": "u_1",
        "balance": 0
      }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 0;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_1' AND kind = 'spend' AND amount = -500;
      """

  @minimum
  Scenario: Insufficient balance is refused and writes nothing
    When POST /wallets/u_1/spend:
      """json
      {
        "body": {
          "amount": 501
        }
      }
      """
    Then response status is 422
    And response body contains:
      """json
      {
        "error": "INSUFFICIENT_BALANCE"
      }
      """
    # The refusal in full: no spend row anywhere, and the balance is the one the
    # Background seeded. This is the pair of assertions a status-code-only test
    # never makes.
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE kind = 'spend';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """

  Scenario: A wallet that has never received credits cannot be spent from
    When POST /wallets/u_nobody/spend:
      """json
      {
        "body": {
          "amount": 1
        }
      }
      """
    Then response status is 422
    And response body contains:
      """json
      {
        "error": "INSUFFICIENT_BALANCE"
      }
      """
    # A refused spend is not a reason to bring a wallet into existence.
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_nobody';
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_nobody';
      """

  Scenario: A negative balance refuses every spend until credits arrive
    Given in PostgreSQL:
      """sql
      UPDATE wallets SET balance = -40 WHERE user_id = 'u_1';
      INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id) VALUES
        ('u_1', 'spend',   -500, NULL,                NULL),
        ('u_1', 'reversal', -40, 'pay_seed000000001', 'evt_seed000000002');
      """
    When POST /wallets/u_1/spend:
      """json
      {
        "body": {
          "amount": 1
        }
      }
      """
    Then response status is 422
    And response body contains:
      """json
      {
        "error": "INSUFFICIENT_BALANCE"
      }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = -40;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_1' HAVING count(*) = 3;
      """

  Scenario: An amount of zero is not a spend
    When POST /wallets/u_1/spend:
      """json
      {
        "body": {
          "amount": 0
        }
      }
      """
    Then response status is 400
    And response body contains:
      """json
      {
        "error": "INVALID_AMOUNT"
      }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE kind = 'spend';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """

  Scenario: A negative amount cannot be used to grant credits
    # The one that matters most of the five: without it, a spend of -1000 is a
    # top-up endpoint that bypasses the payment provider entirely.
    When POST /wallets/u_1/spend:
      """json
      {
        "body": {
          "amount": -1000
        }
      }
      """
    Then response status is 400
    And response body contains:
      """json
      {
        "error": "INVALID_AMOUNT"
      }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE kind = 'spend';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """

  Scenario: A fractional amount is refused rather than rounded
    # Credits are integers. Accepting 12.5 means a truncation or a float
    # somewhere in the arithmetic, and money with a float in it is wrong later
    # rather than wrong now.
    When POST /wallets/u_1/spend:
      """json
      {
        "body": {
          "amount": 12.5
        }
      }
      """
    Then response status is 400
    And response body contains:
      """json
      {
        "error": "INVALID_AMOUNT"
      }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE kind = 'spend';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """

  Scenario: A numeric string is not a number
    When POST /wallets/u_1/spend:
      """json
      {
        "body": {
          "amount": "100"
        }
      }
      """
    Then response status is 400
    And response body contains:
      """json
      {
        "error": "INVALID_AMOUNT"
      }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE kind = 'spend';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """

  Scenario: A request with no amount is refused
    When POST /wallets/u_1/spend:
      """json
      {
        "body": {}
      }
      """
    Then response status is 400
    And response body contains:
      """json
      {
        "error": "INVALID_AMOUNT"
      }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries WHERE kind = 'spend';
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 500;
      """

  Scenario: Retrying a spend with the same idempotency key replays the first answer
    When POST /wallets/u_1/spend:
      """json
      {
        "headers": {
          "Idempotency-Key": "spend-abc-1"
        },
        "body": {
          "amount": 100
        }
      }
      """
    Then response status is 200
    And save response body field "balance" as "firstBalance"
    When POST /wallets/u_1/spend:
      """json
      {
        "headers": {
          "Idempotency-Key": "spend-abc-1"
        },
        "body": {
          "amount": 100
        }
      }
      """
    # The same answer, not a new one computed from the balance the first call
    # left: a service that deducted twice would answer 300 here.
    Then response status is 200
    And response body contains:
      """json
      {
        "user_id": "u_1",
        "balance": "{firstBalance}"
      }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 400;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_1' AND kind = 'spend' HAVING count(*) = 1;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM idempotency_keys WHERE key = 'spend-abc-1' AND user_id = 'u_1';
      """

  Scenario: The same idempotency key with a different amount is refused
    # A key stands for one request. Replaying the stored answer for a different
    # body would answer a question nobody asked.
    When POST /wallets/u_1/spend:
      """json
      {
        "headers": {
          "Idempotency-Key": "spend-abc-1"
        },
        "body": {
          "amount": 100
        }
      }
      """
    Then response status is 200
    When POST /wallets/u_1/spend:
      """json
      {
        "headers": {
          "Idempotency-Key": "spend-abc-1"
        },
        "body": {
          "amount": 250
        }
      }
      """
    Then response status is 422
    And response body contains:
      """json
      {
        "error": "IDEMPOTENCY_KEY_REUSED"
      }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 400;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_1' AND kind = 'spend' HAVING count(*) = 1;
      """

  Scenario: Two spends with different keys are two spends
    When POST /wallets/u_1/spend:
      """json
      {
        "headers": {
          "Idempotency-Key": "spend-abc-1"
        },
        "body": {
          "amount": 100
        }
      }
      """
    Then response status is 200
    When POST /wallets/u_1/spend:
      """json
      {
        "headers": {
          "Idempotency-Key": "spend-abc-2"
        },
        "body": {
          "amount": 100
        }
      }
      """
    Then response status is 200
    And response body contains:
      """json
      {
        "balance": 300
      }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries WHERE user_id = 'u_1' AND kind = 'spend' HAVING count(*) = 2 AND sum(amount) = -200;
      """

  Scenario Outline: Four spends racing for one balance never overdraw it
    # 100 credits, four callers asking for 60 at one instant, different keys, so
    # idempotency has nothing to say: what stands between 4 × 60 and a balance of
    # 100 is the conditional write. A service that reads the balance and then
    # writes lets two through and ends at -20.
    Given in PostgreSQL:
      """sql
      UPDATE wallets SET balance = 100 WHERE user_id = 'u_1';
      INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id) VALUES
        ('u_1', 'spend', -400, NULL, NULL);
      """
    When these things happen at one instant:
      """json
      [
        {"method": "GET", "path": "/health", "body": {}},
        {"method": "GET", "path": "/health", "body": {}},
        {"method": "GET", "path": "/health", "body": {}},
        {"method": "GET", "path": "/health", "body": {}}
      ]
      """
    Then exactly 4 responses are 200
    When these things happen at one instant:
      """json
      [
        {
          "method": "POST",
          "path": "/wallets/u_1/spend",
          "headers": {
            "Idempotency-Key": "race-spend-<run>-a"
          },
          "body": {
            "amount": 60
          }
        },
        {
          "method": "POST",
          "path": "/wallets/u_1/spend",
          "headers": {
            "Idempotency-Key": "race-spend-<run>-b"
          },
          "body": {
            "amount": 60
          }
        },
        {
          "method": "POST",
          "path": "/wallets/u_1/spend",
          "headers": {
            "Idempotency-Key": "race-spend-<run>-c"
          },
          "body": {
            "amount": 60
          }
        },
        {
          "method": "POST",
          "path": "/wallets/u_1/spend",
          "headers": {
            "Idempotency-Key": "race-spend-<run>-d"
          },
          "body": {
            "amount": 60
          }
        }
      ]
      """
    Then exactly 1 response is 200
    And exactly 3 responses are 422
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallets WHERE user_id = 'u_1' AND balance = 40;
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
       WHERE user_id = 'u_1' AND kind = 'spend' AND amount = -60
      HAVING count(*) = 1;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets w
       WHERE w.balance <> COALESCE((SELECT sum(e.amount) FROM wallet_entries e WHERE e.user_id = w.user_id), 0);
      """

    Examples: three runs
      | run |
      | 1   |
      | 2   |
      | 3   |
