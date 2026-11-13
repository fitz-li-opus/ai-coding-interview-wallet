"""Race-condition tests against a real file-backed SQLite DB with real threads.

Every request hits the app concurrently; the assertions are on exact final balances,
so a single lost update would fail them.
"""
import json
from concurrent.futures import ThreadPoolExecutor

import pytest

from app import create_app
from app.config import TestConfig
from app.extensions import db
from app.models import Transaction

from .conftest import make_event, sign

THREADS = 16


@pytest.fixture()
def live_app(tmp_path):
    class FileConfig(TestConfig):
        SQLALCHEMY_DATABASE_URI = f"sqlite:///{tmp_path / 'race.db'}"

    app = create_app(FileConfig)
    yield app
    with app.app_context():
        db.session.remove()
        db.drop_all()


def run_parallel(app, jobs):
    """Run callables(client) at the same time; return their results in order."""
    def wrap(job):
        return job(app.test_client())

    with ThreadPoolExecutor(max_workers=THREADS) as pool:
        return list(pool.map(wrap, jobs))


def webhook(event):
    body = json.dumps(event).encode()

    def job(client):
        return client.post("/webhooks/payments", data=body, headers={"Opus-Signature": sign(body)})

    return job


def spend(user, amount):
    return lambda client: client.post(f"/wallets/{user}/spend", json={"amount": amount})


def balance(app, user="u_1"):
    with app.test_client() as c:
        return c.get(f"/wallets/{user}").get_json()["balance"]


def test_many_payments_for_same_wallet_no_lost_update(live_app):
    jobs = [webhook(make_event(payment=f"pay_{i}", credits=10)) for i in range(60)]
    results = run_parallel(live_app, jobs)
    assert {r.status_code for r in results} == {200}
    assert balance(live_app) == 600
    with live_app.app_context():
        assert Transaction.query.filter_by(type="deposit").count() == 60


def test_same_event_delivered_concurrently_applies_once(live_app):
    event = make_event(payment="pay_same", credits=500, event_id="evt_same")
    results = run_parallel(live_app, [webhook(event) for _ in range(40)])
    assert {r.status_code for r in results} == {200}
    assert balance(live_app) == 500
    with live_app.app_context():
        assert Transaction.query.count() == 1


def test_succeeded_and_refunded_racing_for_same_payment(live_app):
    jobs = []
    for _ in range(20):
        jobs.append(webhook(make_event("payment.succeeded", payment="pay_x", credits=500)))
        jobs.append(webhook(make_event("payment.refunded", payment="pay_x", credits=500)))
    results = run_parallel(live_app, jobs)
    assert {r.status_code for r in results} == {200}
    assert balance(live_app) == 0
    with live_app.app_context():
        assert sorted(t.type for t in Transaction.query.all()) == ["deposit", "refund"]


def test_concurrent_spends_never_overspend(live_app):
    run_parallel(live_app, [webhook(make_event(credits=100))])
    results = run_parallel(live_app, [spend("u_1", 10) for _ in range(40)])
    codes = [r.status_code for r in results]
    assert codes.count(200) == 10 and codes.count(409) == 30
    assert balance(live_app) == 0
    with live_app.app_context():
        assert Transaction.query.filter_by(type="spend").count() == 10


def test_deposits_and_spends_interleaved(live_app):
    run_parallel(live_app, [webhook(make_event(payment="seed", credits=1000))])
    jobs = []
    for i in range(30):
        jobs.append(webhook(make_event(payment=f"pay_{i}", credits=10)))
        jobs.append(spend("u_1", 10))
    results = run_parallel(live_app, jobs)
    assert {r.status_code for r in results} == {200}
    assert balance(live_app) == 1000  # +300 -300, nothing lost


def test_refund_racing_with_spend_stays_consistent(live_app):
    run_parallel(live_app, [webhook(make_event(payment="p", credits=100))])
    jobs = [webhook(make_event("payment.refunded", payment="p", credits=100))] + [
        spend("u_1", 10) for _ in range(10)
    ]
    results = run_parallel(live_app, jobs)
    assert all(r.status_code in (200, 409) for r in results)
    spent = sum(10 for r in results[1:] if r.status_code == 200)
    # refund always applies (-100); successful spends each took 10 -> may end below zero by design
    assert balance(live_app) == 100 - 100 - spent
    with live_app.app_context():
        total = sum(
            {"deposit": 1, "refund": -1, "spend": -1}[t.type] * t.amount for t in Transaction.query.all()
        )
        assert total == balance(live_app)  # ledger always reconciles with the balance
