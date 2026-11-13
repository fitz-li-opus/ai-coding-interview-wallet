import hashlib
import hmac
import itertools
import json
import time

import pytest

from app import create_app
from app.config import TestConfig
from app.extensions import db

SECRET = TestConfig.WEBHOOK_SECRET
_ids = itertools.count(1)


@pytest.fixture()
def app():
    app = create_app(TestConfig)
    with app.app_context():
        yield app
        db.session.remove()
        db.drop_all()


@pytest.fixture()
def client(app):
    return app.test_client()


def sign(body: bytes, *, secret: str = SECRET, t: int | None = None) -> str:
    t = int(time.time()) if t is None else t
    digest = hmac.new(secret.encode(), f"{t}.".encode() + body, hashlib.sha256).hexdigest()
    return f"t={t},v1={digest}"


def make_event(type_="payment.succeeded", *, payment="pay_1", user="u_1", credits=500, event_id=None):
    return {
        "id": event_id or f"evt_{next(_ids)}",
        "type": type_,
        "created": int(time.time()),
        "data": {
            "payment": {
                "id": payment,
                "user_id": user,
                "amount_cents": credits,
                "credits": credits,
                "status": "refunded" if type_ == "payment.refunded" else "succeeded",
            }
        },
    }


@pytest.fixture()
def send(client):
    """POST a signed event the way the provider does."""

    def _send(event, *, signature=None):
        body = json.dumps(event).encode()
        headers = {"Content-Type": "application/json", "Opus-Signature": signature or sign(body)}
        return client.post("/webhooks/payments", data=body, headers=headers)

    return _send


@pytest.fixture()
def balance(client):
    return lambda user="u_1": client.get(f"/wallets/{user}").get_json()["balance"]
