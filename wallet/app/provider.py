"""Webhook protocol of the payment provider.

- `POST /webhooks/payments`, JSON body, delivered at-least-once and possibly out of order.
- `Opus-Signature: t=<unix ts>,v1=<hex>` with v1 = HMAC-SHA256(secret, "<t>.<raw body>").
"""
import hashlib
import hmac
import json
import time
from dataclasses import dataclass

SUCCEEDED = "payment.succeeded"
REFUNDED = "payment.refunded"
SUPPORTED_TYPES = (SUCCEEDED, REFUNDED)


class SignatureError(Exception):
    """Signature header missing, malformed, stale or not matching the body."""


class PayloadError(ValueError):
    """Body is not a valid event."""


@dataclass(frozen=True)
class ProviderEvent:
    event_id: str
    type: str
    # Set for SUPPORTED_TYPES only.
    payment_id: str | None = None
    user_id: str | None = None
    credits: int | None = None


def verify_signature(
    secret: str, header: str | None, body: bytes, *, tolerance: int = 300, now: float | None = None
) -> None:
    if not secret:  # fail closed: a config gap must not turn into "accept anything"
        raise SignatureError("Webhook secret is not configured")
    if not header:
        raise SignatureError("Missing Opus-Signature header")

    timestamp, candidates = None, []
    for item in header.split(","):
        key, _, value = item.strip().partition("=")
        if key == "t":
            timestamp = value
        elif key == "v1":
            candidates.append(value)
    try:
        signed_at = int(timestamp)
    except (TypeError, ValueError):
        raise SignatureError("Malformed Opus-Signature header") from None

    if abs((time.time() if now is None else now) - signed_at) > tolerance:
        raise SignatureError("Signature timestamp outside tolerance")

    expected = hmac.new(secret.encode(), f"{timestamp}.".encode() + body, hashlib.sha256).hexdigest()
    if not any(hmac.compare_digest(expected.encode(), c.encode()) for c in candidates):
        raise SignatureError("Signature mismatch")


def parse_event(body: bytes) -> ProviderEvent:
    try:
        data = json.loads(body)
    except (json.JSONDecodeError, UnicodeDecodeError):
        raise PayloadError("Body is not valid JSON") from None
    if not isinstance(data, dict):
        raise PayloadError("Body must be a JSON object")

    event_id, event_type = data.get("id"), data.get("type")
    if not isinstance(event_id, str) or not event_id or not isinstance(event_type, str):
        raise PayloadError("'id' and 'type' are required strings")
    if event_type not in SUPPORTED_TYPES:
        return ProviderEvent(event_id=event_id, type=event_type)

    inner = data.get("data")
    payment = inner.get("payment") if isinstance(inner, dict) else None
    if not isinstance(payment, dict):
        raise PayloadError("'data.payment' is required")
    payment_id, user_id, credits = payment.get("id"), payment.get("user_id"), payment.get("credits")
    if not isinstance(payment_id, str) or not payment_id:
        raise PayloadError("'payment.id' must be a non-empty string")
    if not isinstance(user_id, str) or not user_id:
        raise PayloadError("'payment.user_id' must be a non-empty string")
    if not isinstance(credits, int) or isinstance(credits, bool) or credits <= 0:
        raise PayloadError("'payment.credits' must be a positive integer")

    return ProviderEvent(
        event_id=event_id, type=event_type, payment_id=payment_id, user_id=user_id, credits=credits
    )
