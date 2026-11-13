from flask import Blueprint, current_app, jsonify, request

from . import service
from .errors import APIError
from .provider import PayloadError, SignatureError, parse_event, verify_signature

bp = Blueprint("main", __name__)


@bp.get("/health")
def health():
    return jsonify({"status": "ok"})


@bp.post("/webhooks/payments")
def payments_webhook():
    body = request.get_data()  # the signature covers the raw bytes
    try:
        verify_signature(
            current_app.config["WEBHOOK_SECRET"],
            request.headers.get("Opus-Signature"),
            body,
            tolerance=current_app.config["WEBHOOK_TOLERANCE_SECONDS"],
        )
    except SignatureError as e:
        raise APIError(str(e), code="invalid_signature", status_code=400)
    try:
        event = parse_event(body)
    except PayloadError as e:
        raise APIError(str(e), code="invalid_payload", status_code=400)

    service.apply_event(event)
    return jsonify({"received": True})


@bp.get("/wallets/<user_id>")
def get_wallet(user_id: str):
    return jsonify({"user_id": user_id, "balance": service.get_balance(user_id)})


@bp.post("/wallets/<user_id>/spend")
def spend(user_id: str):
    data = request.get_json(silent=True)
    amount = data.get("amount") if isinstance(data, dict) else None
    if not isinstance(amount, int) or isinstance(amount, bool) or amount <= 0:
        raise APIError("'amount' must be a positive integer", code="invalid_amount")
    return jsonify({"user_id": user_id, "balance": service.spend(user_id, amount)})
