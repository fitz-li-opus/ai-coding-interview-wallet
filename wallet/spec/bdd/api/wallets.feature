@wallets
Feature: Reading a wallet

  A wallet is not a row somebody creates. It is a question about a user, and it
  has an answer before any payment has ever arrived for them: zero. Credits
  arrive through a webhook for a `user_id` this service has never seen, so a
  wallet that had to be created before it could be read would turn the first
  payment of every customer into an error.

  Two rules hold here, and each has a scenario that removes it:

    **A balance is a derived number, not a stored opinion.** Every movement is
    an append-only row in `wallet_entries` — `credit` from a succeeded payment,
    `spend` from the user, `reversal` from a refund — and `wallets.balance` is
    the same sum kept in one place so a read costs one row and so the database
    itself can refuse a spend that would go below zero. Both are written in one
    transaction, so "the balance disagrees with the ledger" is not a state this
    service passes through. The invariant scenario is the one that says so, for
    every user at once, and it is the scenario that catches a balance updated
    without its entry.
    **Reading never writes.** Asking about an unknown user returns zero and
    leaves the tables exactly as empty as they were. The scenario that asserts
    `0 rows` in `wallets` afterwards is the one that catches a read path that
    upserts for convenience.

  A negative balance is not an error state either, and it is reported as it is.
  It is what a refund of credits that were already spent means: the money went
  back to the customer, so the credits have to come back from the wallet, and
  pretending the floor is zero would make the ledger lie about it. What a
  negative balance does change is what the user may do next, and that rule
  lives in spend.feature.

  Background:
    Given run migration

  Scenario: An unknown user has a zero balance and is not created by being read
    When GET /wallets/u_nobody:
      """json
      {
        "body": {}
      }
      """
    Then response status is 200
    And response body contains:
      """json
      {
        "user_id": "u_nobody",
        "balance": 0
      }
      """
    # Reading is not a write. A service that upserted here would pass the
    # assertion above and fail these two.
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets;
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallet_entries;
      """

  @minimum
  Scenario: A wallet reports the credits that arrived for it
    Given in PostgreSQL:
      """sql
      INSERT INTO wallets (user_id, balance) VALUES ('u_1', 500);
      INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id) VALUES
        ('u_1', 'credit', 500, 'pay_seed000000001', 'evt_seed000000001');
      """
    When GET /wallets/u_1:
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
        "balance": 500
      }
      """

  @minimum
  Scenario: Credits that arrive from the payment provider are visible in the balance
    # The only way credits enter this system. The provider signs and delivers on
    # its own schedule, so the assertion waits for delivery to settle rather
    # than for a clock.
    Given payment provider creates a payment as "payment":
      """json
      {
        "user_id": "u_1",
        "amount_cents": 500
      }
      """
    When background work has settled
    Then exactly 1 webhook delivery was accepted
    When GET /wallets/u_1:
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
        "balance": 500
      }
      """
    And in PostgreSQL query returns 1 row:
      """sql
      SELECT 1 FROM wallet_entries
       WHERE user_id = 'u_1' AND kind = 'credit' AND amount = 500 AND payment_id = '{payment}'
      HAVING count(*) = 1;
      """

  Scenario: One user's credits are not another user's credits
    Given in PostgreSQL:
      """sql
      INSERT INTO wallets (user_id, balance) VALUES ('u_1', 500), ('u_2', 20);
      INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id) VALUES
        ('u_1', 'credit', 500, 'pay_seed000000001', 'evt_seed000000001'),
        ('u_2', 'credit',  20, 'pay_seed000000002', 'evt_seed000000002');
      """
    When GET /wallets/u_2:
      """json
      {
        "body": {}
      }
      """
    Then response status is 200
    And response body contains:
      """json
      {
        "user_id": "u_2",
        "balance": 20
      }
      """
    And response body does not contain "500"

  Scenario: Every balance equals the sum of that user's ledger
    # The invariant, asserted for every user at once rather than for the one
    # this scenario touched. Seeded state, a credit from the provider and a
    # spend all have to satisfy it together.
    Given in PostgreSQL:
      """sql
      INSERT INTO wallets (user_id, balance) VALUES ('u_1', 500), ('u_2', 0), ('u_3', -40);
      INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id) VALUES
        ('u_1', 'credit',   500, 'pay_seed000000001', 'evt_seed000000001'),
        ('u_3', 'credit',   100, 'pay_seed000000003', 'evt_seed000000003'),
        ('u_3', 'spend',   -100, NULL,                NULL),
        ('u_3', 'reversal', -40, 'pay_seed000000003', 'evt_seed000000004');
      """
    When POST /wallets/u_1/spend:
      """json
      {
        "body": {
          "amount": 120
        }
      }
      """
    Then response status is 200
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets w
       WHERE w.balance <> COALESCE((SELECT sum(e.amount) FROM wallet_entries e WHERE e.user_id = w.user_id), 0);
      """

  Scenario: A wallet whose credits were refunded after they were spent reports a negative balance
    # u_3 bought 100 credits, spent all of them, and the payment was refunded.
    # The credits are gone and the money went back; -40 is the honest answer and
    # clamping it to 0 would be the ledger lying about a reversal that happened.
    Given in PostgreSQL:
      """sql
      INSERT INTO wallets (user_id, balance) VALUES ('u_3', -40);
      INSERT INTO wallet_entries (user_id, kind, amount, payment_id, event_id) VALUES
        ('u_3', 'credit',   100, 'pay_seed000000003', 'evt_seed000000003'),
        ('u_3', 'spend',   -100, NULL,                NULL),
        ('u_3', 'reversal', -40, 'pay_seed000000003', 'evt_seed000000004');
      """
    When GET /wallets/u_3:
      """json
      {
        "body": {}
      }
      """
    Then response status is 200
    And response body contains:
      """json
      {
        "user_id": "u_3",
        "balance": -40
      }
      """

  Scenario: A user with no movements has an empty ledger rather than an error
    When GET /wallets/u_nobody/entries:
      """json
      {
        "body": {}
      }
      """
    Then response status is 200
    And response body contains:
      """json
      {
        "user_id": "u_nobody",
        "balance": 0,
        "entries": []
      }
      """
    And in PostgreSQL query returns 0 rows:
      """sql
      SELECT 1 FROM wallets;
      """
