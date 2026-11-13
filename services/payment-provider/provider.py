"""
Mock payment provider (Stripe-like) for the Credit Wallet interview.

- POST /payments                 create a payment; a signed `payment.succeeded` webhook follows
- POST /payments/{id}/refund     refund a payment; a signed `payment.refunded` webhook follows
- POST /events/{id}/resend       redeliver an event (like "Resend" in a provider dashboard)
- GET  /events                   list events and their delivery attempts
- GET  /health

Webhooks are delivered at-least-once to WEBHOOK_URL with retries.
Python standard library only.
"""

import hashlib
import hmac
import json
import os
import random
import threading
import time
import urllib.error
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("PORT", "8090"))
WEBHOOK_URL = os.environ.get("WEBHOOK_URL", "http://localhost:8080/webhooks/payments")
WEBHOOK_SECRET = os.environ.get("WEBHOOK_SECRET", "whsec_interview_test")
DUPLICATE_RATE = float(os.environ.get("DUPLICATE_RATE", "0.3"))
RETRY_DELAYS = [1, 2, 4, 8, 16]
DELIVERY_TIMEOUT = 5

payments = {}
events = {}
lock = threading.Lock()


def new_id(prefix):
    return f"{prefix}_{uuid.uuid4().hex[:16]}"


def sign(payload: bytes, timestamp: int) -> str:
    signed = f"{timestamp}.".encode() + payload
    digest = hmac.new(WEBHOOK_SECRET.encode(), signed, hashlib.sha256).hexdigest()
    return f"t={timestamp},v1={digest}"


def deliver_once(event):
    payload = json.dumps(event).encode()
    req = urllib.request.Request(
        WEBHOOK_URL,
        data=payload,
        method="POST",
        headers={
            "Content-Type": "application/json",
            "Opus-Signature": sign(payload, int(time.time())),
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=DELIVERY_TIMEOUT) as resp:
            status = resp.status
    except urllib.error.HTTPError as e:
        status = e.code
    except Exception as e:  # connection refused, timeout, ...
        status = f"error: {e.__class__.__name__}"
    with lock:
        events[event["id"]]["_attempts"].append({"at": int(time.time()), "status": status})
    return isinstance(status, int) and 200 <= status < 300


def deliver_with_retries(event):
    for delay in [0] + RETRY_DELAYS:
        time.sleep(delay)
        if deliver_once(event):
            return
    print(f"[provider] gave up delivering {event['id']}")


def emit(event_type, data, delay=1.0):
    event = {"id": new_id("evt"), "type": event_type, "created": int(time.time()), "data": data}
    with lock:
        events[event["id"]] = {**event, "_attempts": []}

    def run():
        time.sleep(delay)
        deliver_with_retries(event)
        # At-least-once delivery: the same event is sometimes delivered again.
        if random.random() < DUPLICATE_RATE:
            time.sleep(random.uniform(0.1, 1.5))
            deliver_once(event)

    threading.Thread(target=run, daemon=True).start()
    return event


class Handler(BaseHTTPRequestHandler):
    def _send(self, status, body):
        payload = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def _body(self):
        length = int(self.headers.get("Content-Length") or 0)
        if not length:
            return {}
        try:
            body = json.loads(self.rfile.read(length))
        except json.JSONDecodeError:
            return None
        return body if isinstance(body, dict) else None

    def do_GET(self):
        path = self.path.split("?")[0].rstrip("/")
        if path == "/health":
            return self._send(200, {"status": "healthy", "service": "payment-provider"})
        if path == "/events":
            with lock:
                return self._send(200, {"data": list(events.values())})
        self._send(404, {"error": "not found"})

    def do_POST(self):
        parts = [p for p in self.path.split("?")[0].split("/") if p]

        if parts == ["payments"]:
            body = self._body()
            if not body or not body.get("user_id") or type(body.get("amount_cents")) is not int or body["amount_cents"] <= 0:
                return self._send(400, {"error": "user_id and positive integer amount_cents are required"})
            payment = {
                "id": new_id("pay"),
                "user_id": str(body["user_id"]),
                "amount_cents": body["amount_cents"],
                "credits": body["amount_cents"],  # 1 cent buys 1 credit
                "status": "succeeded",
            }
            with lock:
                payments[payment["id"]] = payment
            emit("payment.succeeded", {"payment": payment})
            return self._send(201, payment)

        if len(parts) == 3 and parts[0] == "payments" and parts[2] == "refund":
            with lock:
                payment = payments.get(parts[1])
                if not payment:
                    return self._send(404, {"error": "payment not found"})
                if payment["status"] == "refunded":
                    return self._send(409, {"error": "payment already refunded"})
                payment["status"] = "refunded"
                snapshot = dict(payment)
            emit("payment.refunded", {"payment": snapshot})
            return self._send(200, snapshot)

        if len(parts) == 3 and parts[0] == "events" and parts[2] == "resend":
            with lock:
                stored = events.get(parts[1])
            if not stored:
                return self._send(404, {"error": "event not found"})
            event = {k: v for k, v in stored.items() if not k.startswith("_")}
            threading.Thread(target=deliver_once, args=(event,), daemon=True).start()
            return self._send(202, {"resent": event["id"]})

        self._send(404, {"error": "not found"})

    def log_message(self, fmt, *args):
        print(f"[provider] {self.address_string()} {fmt % args}")


if __name__ == "__main__":
    print(f"payment-provider listening on :{PORT}, delivering webhooks to {WEBHOOK_URL}")
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
