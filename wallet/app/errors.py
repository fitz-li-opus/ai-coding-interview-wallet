from flask import Flask, jsonify
from werkzeug.exceptions import HTTPException


class APIError(Exception):
    status_code = 400
    code = "bad_request"

    def __init__(self, message: str, *, code: str | None = None, status_code: int | None = None):
        super().__init__(message)
        self.message = message
        if code:
            self.code = code
        if status_code:
            self.status_code = status_code


class InsufficientBalance(APIError):
    status_code = 409
    code = "insufficient_balance"


def _render(message: str, code: str, status: int):
    return jsonify({"error": {"code": code, "message": message}}), status


def register_error_handlers(app: Flask) -> None:
    @app.errorhandler(APIError)
    def handle_api_error(e: APIError):
        return _render(e.message, e.code, e.status_code)

    @app.errorhandler(HTTPException)
    def handle_http_error(e: HTTPException):
        return _render(e.description, (e.name or "error").lower().replace(" ", "_"), e.code or 500)
