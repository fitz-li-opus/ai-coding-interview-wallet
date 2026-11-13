#!/usr/bin/env python3
"""
Live checks the interviewer runs against a candidate's wallet service (~1 minute).

Sends signed webhooks straight to the wallet, so the payment provider does not
need to be running. Python standard library only.

    python3 review/checks.py                      # wallet on http://localhost:8080
    python3 review/checks.py --url http://host:8080 --secret whsec_interview_test
"""

import argparse
import hashlib
import hmac
import json
import time
import urllib.error
import urllib.request
import uuid
from concurrent.futures import ThreadPoolExecutor

BASE = "http://localhost:8080"
SECRET = "whsec_interview_test"
RUN = uuid.uuid4().hex[:6]


def uid(name):
    return f"chk_{RUN}_{name}"


def request(method, path, body=None, headers=None):
    data = body if isinstance(body, bytes) else (json.dumps(body).encode() if body is not None else None)
    req = urllib.request.Request(BASE + path, data=data, method=method, headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            raw = resp.read()
            status = resp.status
    except urllib.error.HTTPError as e:
        raw, status = e.read(), e.code
    except Exception as e:
        return None, {"error": str(e)}
    try:
        return status, json.loads(raw or b"{}")
    except json.JSONDecodeError:
        return status, {"raw": raw.decode(errors="replace")}


def signature(payload, ts=None, secret=None):
    ts = int(time.time()) if ts is None else ts
    digest = hmac.new((secret or SECRET).encode(), f"{ts}.".encode() + payload, hashlib.sha256).hexdigest()
    return f"t={ts},v1={digest}"


def event(event_type, user_id, credits, payment_id=None, event_id=None):
    payment_id = payment_id or f"pay_{uuid.uuid4().hex[:16]}"
    return {
        "id": event_id or f"evt_{uuid.uuid4().hex[:16]}",
        "type": event_type,
        "created": int(time.time()),
        "data": {"payment": {
            "id": payment_id, "user_id": user_id, "amount_cents": credits, "credits": credits,
            "status": "refunded" if event_type == "payment.refunded" else "succeeded",
        }},
    }


def send(evt, ts=None, secret=None, sig=None):
    payload = json.dumps(evt).encode()
    header = sig if sig is not None else signature(payload, ts, secret)
    status, _ = request("POST", "/webhooks/payments", payload, {"Opus-Signature": header})
    return status


def balance(user_id):
    _, body = request("GET", f"/wallets/{user_id}")
    return body.get("balance")


def spend(user_id, amount, headers=None):
    status, _ = request("POST", f"/wallets/{user_id}/spend", {"amount": amount}, headers)
    return status


def ok(status):
    return status is not None and 200 <= status < 300


def grant(user_id, credits):
    send(event("payment.succeeded", user_id, credits))


# --- Checks. Each returns (verdict, detail); verdict is PASS / FAIL / INFO. ---

def check_happy_path():
    u = uid("happy")
    s = send(event("payment.succeeded", u, 500))
    b = balance(u)
    return ("PASS" if ok(s) and b == 500 else "FAIL"), f"webhook -> {s}, balance {b} (expect 500)"


def check_duplicate_event():
    u = uid("dup")
    evt = event("payment.succeeded", u, 300)
    statuses = [send(evt) for _ in range(3)]
    b = balance(u)
    return ("PASS" if b == 300 and all(ok(s) for s in statuses) else "FAIL"), \
        f"same event sent 3x -> {statuses}, balance {b} (expect 300, and 2xx every time so the provider stops retrying)"


def check_concurrent_duplicate():
    u = uid("dup_race")
    evt = event("payment.succeeded", u, 200)
    with ThreadPoolExecutor(10) as pool:
        list(pool.map(lambda _: send(evt), range(10)))
    b = balance(u)
    return ("PASS" if b == 200 else "FAIL"), f"same event sent 10x in parallel -> balance {b} (expect 200)"


def check_bad_signature():
    u = uid("badsig")
    s1 = send(event("payment.succeeded", u, 999), secret="whsec_wrong")
    s2 = send(event("payment.succeeded", u, 999), sig="")
    b = balance(u)
    rejected = s1 is not None and 400 <= s1 < 500 and s2 is not None and 400 <= s2 < 500
    return ("PASS" if rejected and not b else "FAIL"), f"wrong secret -> {s1}, missing header -> {s2}, balance {b} (expect 4xx and 0)"


def check_tampered_body():
    u = uid("tamper")
    evt = event("payment.succeeded", u, 1)
    header = signature(json.dumps(evt).encode())
    evt["data"]["payment"]["credits"] = 1_000_000
    s = send(evt, sig=header)
    b = balance(u)
    return ("PASS" if s and 400 <= s < 500 and not b else "FAIL"), f"credits changed after signing -> {s}, balance {b} (expect 4xx and 0)"


def check_replay_old_timestamp():
    u = uid("replay")
    s = send(event("payment.succeeded", u, 100), ts=int(time.time()) - 3600)
    b = balance(u)
    return ("PASS" if s and 400 <= s < 500 and not b else "INFO"), \
        f"validly signed, 1h-old timestamp -> {s}, balance {b} (bonus: reject stale signatures)"


def check_overspend():
    u = uid("over")
    grant(u, 100)
    s = spend(u, 150)
    b = balance(u)
    return ("PASS" if s and 400 <= s < 500 and b == 100 else "FAIL"), f"spend 150 of 100 -> {s}, balance {b} (expect 4xx and 100)"


def check_invalid_amount():
    u = uid("invalid")
    grant(u, 100)
    results = {amt: spend(u, amt) for amt in (-50, 0, "10")}
    b = balance(u)
    rejected = all(s and 400 <= s < 500 for s in results.values())
    return ("PASS" if rejected and b == 100 else "FAIL"), f"spend -50 / 0 / \"10\" -> {list(results.values())}, balance {b} (expect 4xx and 100)"


def check_concurrent_spend():
    u = uid("race")
    grant(u, 1000)
    with ThreadPoolExecutor(50) as pool:
        statuses = list(pool.map(lambda _: spend(u, 100), range(50)))
    wins = sum(ok(s) for s in statuses)
    b = balance(u)
    return ("PASS" if wins == 10 and b == 0 else "FAIL"), f"50 parallel spends of 100 on 1000 -> {wins} succeeded, balance {b} (expect 10 and 0)"


def check_idempotent_spend():
    u = uid("idem")
    grant(u, 500)
    key = {"Idempotency-Key": uuid.uuid4().hex}
    s1, s2 = spend(u, 100, key), spend(u, 100, key)
    b = balance(u)
    return ("PASS" if b == 400 else "INFO"), f"same Idempotency-Key twice -> {s1}, {s2}, balance {b} (bonus: 400)"


def check_refund():
    u = uid("refund")
    pay = f"pay_{uuid.uuid4().hex[:16]}"
    send(event("payment.succeeded", u, 500, payment_id=pay))
    s = send(event("payment.refunded", u, 500, payment_id=pay))
    b = balance(u)
    return ("PASS" if ok(s) and b == 0 else "INFO"), f"buy 500 then refund -> {s}, balance {b} (bonus: 0)"


def check_refund_after_spend():
    u = uid("refund_spent")
    pay = f"pay_{uuid.uuid4().hex[:16]}"
    send(event("payment.succeeded", u, 500, payment_id=pay))
    spend(u, 400)
    s = send(event("payment.refunded", u, 500, payment_id=pay))
    b = balance(u)
    return "INFO", f"buy 500, spend 400, refund -> {s}, balance {b}. Ask: negative balance, clamp to 0, or flag for review?"


def check_refund_before_payment():
    u = uid("ooo")
    pay = f"pay_{uuid.uuid4().hex[:16]}"
    s1 = send(event("payment.refunded", u, 500, payment_id=pay))
    s2 = send(event("payment.succeeded", u, 500, payment_id=pay))
    b = balance(u)
    return ("PASS" if b == 0 else "INFO"), f"refund arrives before payment -> {s1}, {s2}, balance {b} (bonus: 0)"


def check_unknown_event():
    s = send(event("customer.updated", uid("unknown"), 0))
    return ("PASS" if ok(s) else "INFO"), f"unknown event type -> {s} (bonus: 2xx so the provider stops retrying)"


CHECKS = [
    ("Core", "Webhook adds credits", check_happy_path),
    ("Core", "Overspend rejected", check_overspend),
    ("Core", "Invalid amounts rejected", check_invalid_amount),
    ("Idempotency", "Duplicate webhook credited once", check_duplicate_event),
    ("Idempotency", "Parallel duplicate webhooks credited once", check_concurrent_duplicate),
    ("Concurrency", "Parallel spends never overdraw", check_concurrent_spend),
    ("Security", "Bad or missing signature rejected", check_bad_signature),
    ("Security", "Tampered body rejected", check_tampered_body),
    ("Bonus", "Stale timestamp rejected", check_replay_old_timestamp),
    ("Bonus", "Idempotent spend", check_idempotent_spend),
    ("Bonus", "Refund claws back credits", check_refund),
    ("Bonus", "Refund arrives before payment", check_refund_before_payment),
    ("Bonus", "Unknown event acknowledged", check_unknown_event),
    ("Discuss", "Refund after credits are spent", check_refund_after_spend),
]


def main():
    global BASE, SECRET
    parser = argparse.ArgumentParser(description="Run live checks against a candidate's wallet service")
    parser.add_argument("--url", default=BASE)
    parser.add_argument("--secret", default=SECRET)
    args = parser.parse_args()
    BASE, SECRET = args.url.rstrip("/"), args.secret

    if request("GET", f"/wallets/{uid('ping')}") is None:
        raise SystemExit(f"Cannot reach the wallet service at {BASE}")

    icons = {"PASS": "✅", "FAIL": "❌", "INFO": "ℹ️ "}
    passed = core = 0
    for group, name, fn in CHECKS:
        try:
            verdict, detail = fn()
        except Exception as e:
            verdict, detail = "FAIL", f"check crashed: {e!r}"
        if group not in ("Bonus", "Discuss"):
            core += 1
            passed += verdict == "PASS"
        print(f"{icons} [{group}] {name}\n     {detail}")
    print(f"\nRequired checks passed: {passed}/{core}")


if __name__ == "__main__":
    main()