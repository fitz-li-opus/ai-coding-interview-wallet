from app.models import Transaction

from .conftest import make_event


def test_health(client):
    assert client.get("/health").get_json() == {"status": "ok"}


def test_get_wallet(send, client):
    assert client.get("/wallets/u_1").get_json() == {"user_id": "u_1", "balance": 0}  # unknown user
    send(make_event())
    assert client.get("/wallets/u_1").get_json() == {"user_id": "u_1", "balance": 500}


def test_spend_returns_new_balance(send, client, balance):
    send(make_event())
    r = client.post("/wallets/u_1/spend", json={"amount": 100})
    assert r.status_code == 200
    assert r.get_json() == {"user_id": "u_1", "balance": 400}
    assert balance() == 400
    assert Transaction.query.filter_by(type="spend").count() == 1


def test_spend_exact_balance(send, client):
    send(make_event())
    assert client.post("/wallets/u_1/spend", json={"amount": 500}).get_json()["balance"] == 0


def test_spend_insufficient_changes_nothing(send, client, balance):
    send(make_event())
    r = client.post("/wallets/u_1/spend", json={"amount": 501})
    assert r.status_code == 409 and r.get_json()["error"]["code"] == "insufficient_balance"
    assert balance() == 500
    assert Transaction.query.filter_by(type="spend").count() == 0


def test_spend_unknown_user_is_insufficient(client):
    assert client.post("/wallets/nobody/spend", json={"amount": 1}).status_code == 409


def test_spend_validation(client):
    for body in ({"amount": 0}, {"amount": -1}, {"amount": 1.5}, {"amount": "10"}, {"amount": True}, {}, []):
        assert client.post("/wallets/u_1/spend", json=body).status_code == 400, body
    assert client.post("/wallets/u_1/spend", data="nope").status_code == 400
