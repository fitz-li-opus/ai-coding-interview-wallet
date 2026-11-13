import json
import time

from app.extensions import db
from app.models import Transaction

from .conftest import make_event, sign


def test_succeeded_credits_wallet_and_creates_it(send, balance):
    assert balance() == 0
    r = send(make_event())
    assert r.status_code == 200 and r.get_json() == {"received": True}
    assert balance() == 500


def test_same_event_delivered_twice_credits_once(send, balance):
    event = make_event(event_id="evt_dup")
    assert send(event).status_code == 200
    assert send(event).status_code == 200
    assert balance() == 500
    assert Transaction.query.count() == 1


def test_resend_with_new_event_id_credits_once(send, balance):
    send(make_event(event_id="evt_a"))
    send(make_event(event_id="evt_b"))  # same payment
    assert balance() == 500


def test_refund_debits(send, balance):
    send(make_event())
    assert send(make_event("payment.refunded")).status_code == 200
    assert balance() == 0
    assert send(make_event("payment.refunded")).status_code == 200  # duplicate refund
    assert balance() == 0


def test_refund_before_succeeded_then_succeeded_is_noop(send, balance):
    send(make_event("payment.refunded"))
    assert balance() == 0
    send(make_event("payment.succeeded"))  # late
    assert balance() == 0
    assert {t.type for t in Transaction.query.all()} == {"deposit", "refund"}


def test_refund_after_spend_goes_negative_and_blocks_spending(send, client, balance):
    send(make_event())
    client.post("/wallets/u_1/spend", json={"amount": 400})
    send(make_event("payment.refunded"))
    assert balance() == -400
    assert client.post("/wallets/u_1/spend", json={"amount": 1}).status_code == 409


def test_users_are_independent(send, balance):
    send(make_event(payment="p1", user="u_1", credits=100))
    send(make_event(payment="p2", user="u_2", credits=250))
    assert balance("u_1") == 100 and balance("u_2") == 250


def test_unknown_event_type_is_acknowledged_and_ignored(client, send, balance):
    r = send({"id": "evt_x", "type": "payment.disputed", "data": {}})
    assert r.status_code == 200
    assert Transaction.query.count() == 0


# ---- authenticity / validation


def test_missing_signature_rejected(client, balance):
    body = json.dumps(make_event()).encode()
    r = client.post("/webhooks/payments", data=body)
    assert r.status_code == 400 and r.get_json()["error"]["code"] == "invalid_signature"
    assert balance() == 0


def test_wrong_secret_rejected(send, balance):
    event = make_event()
    r = send(event, signature=sign(json.dumps(event).encode(), secret="whsec_other"))
    assert r.status_code == 400
    assert balance() == 0


def test_tampered_body_rejected(client, balance):
    body = json.dumps(make_event(credits=500)).encode()
    sig = sign(body)
    tampered = json.dumps(make_event(credits=999999)).encode()
    r = client.post("/webhooks/payments", data=tampered, headers={"Opus-Signature": sig})
    assert r.status_code == 400
    assert balance() == 0


def test_stale_timestamp_rejected(client, balance):
    body = json.dumps(make_event()).encode()
    sig = sign(body, t=int(time.time()) - 3600)
    r = client.post("/webhooks/payments", data=body, headers={"Opus-Signature": sig})
    assert r.status_code == 400
    assert balance() == 0


def test_malformed_signature_header_rejected(client):
    r = client.post("/webhooks/payments", data=b"{}", headers={"Opus-Signature": "garbage"})
    assert r.status_code == 400


def test_invalid_payloads_rejected(client):
    def post(body: bytes):
        return client.post("/webhooks/payments", data=body, headers={"Opus-Signature": sign(body)})

    assert post(b"not json").get_json()["error"]["code"] == "invalid_payload"
    assert post(b"[]").status_code == 400
    assert post(json.dumps({"type": "payment.succeeded"}).encode()).status_code == 400
    bad = make_event()
    bad["data"]["payment"]["credits"] = -5
    assert post(json.dumps(bad).encode()).status_code == 400
    bad["data"]["payment"]["credits"] = "500"
    assert post(json.dumps(bad).encode()).status_code == 400
    assert Transaction.query.count() == 0
