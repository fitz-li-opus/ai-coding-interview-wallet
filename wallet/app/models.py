from datetime import datetime, timezone

from .extensions import db


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


class Wallet(db.Model):
    """One row per user. `balance` is in credits (1 cent = 1 credit)."""

    __tablename__ = "wallets"

    user_id = db.Column(db.String(64), primary_key=True)
    # No CHECK >= 0: a provider refund of already-spent credits can legitimately
    # leave a negative balance. Spending is guarded in service.spend().
    balance = db.Column(db.BigInteger, nullable=False, default=0)
    created_at = db.Column(db.DateTime(timezone=True), nullable=False, default=utcnow)


class Transaction(db.Model):
    """Append-only ledger. `ref` is unique, which makes event processing idempotent."""

    __tablename__ = "transactions"

    id = db.Column(db.Integer, primary_key=True)
    user_id = db.Column(db.String(64), db.ForeignKey("wallets.user_id"), nullable=False, index=True)
    type = db.Column(db.String(16), nullable=False)  # deposit | refund | spend
    amount = db.Column(db.BigInteger, nullable=False)  # credits, always positive
    # e.g. "payment:pay_123:deposit". NULL for spends (SQLite allows many NULLs in a UNIQUE column).
    ref = db.Column(db.String(128), unique=True)
    created_at = db.Column(db.DateTime(timezone=True), nullable=False, default=utcnow)

    __table_args__ = (db.CheckConstraint("amount > 0", name="ck_transaction_amount_positive"),)
