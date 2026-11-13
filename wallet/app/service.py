"""Wallet logic. Every balance change is an atomic SQL UPDATE plus a ledger row,
committed together, so concurrent requests cannot lose updates or overspend."""
from sqlalchemy import select, update
from sqlalchemy.dialects.sqlite import insert
from sqlalchemy.exc import IntegrityError

from .errors import InsufficientBalance
from .extensions import db
from .models import Transaction, Wallet
from .provider import REFUNDED, SUPPORTED_TYPES, ProviderEvent


def get_balance(user_id: str) -> int:
    return db.session.scalar(select(Wallet.balance).where(Wallet.user_id == user_id)) or 0


def spend(user_id: str, amount: int) -> int:
    """Deduct `amount` credits; returns the new balance. Raises InsufficientBalance."""
    result = db.session.execute(
        update(Wallet)
        .where(Wallet.user_id == user_id, Wallet.balance >= amount)  # check + deduct in one statement
        .values(balance=Wallet.balance - amount)
    )
    if result.rowcount == 0:  # unknown wallet counts as balance 0
        db.session.rollback()
        raise InsufficientBalance("Not enough credits")
    db.session.add(Transaction(user_id=user_id, type="spend", amount=amount))
    new_balance = get_balance(user_id)  # same DB transaction, so exactly the post-spend value
    db.session.commit()
    return new_balance


def _post(user_id: str, type: str, amount: int, ref: str, delta: int) -> None:
    """Record a ledger entry and move the balance, unless `ref` was already applied."""
    if db.session.scalar(select(Transaction.id).where(Transaction.ref == ref)) is not None:
        return
    db.session.add(Transaction(user_id=user_id, type=type, amount=amount, ref=ref))
    db.session.flush()
    db.session.execute(
        update(Wallet).where(Wallet.user_id == user_id).values(balance=Wallet.balance + delta)
    )


def apply_event(event: ProviderEvent) -> None:
    """Apply a verified provider event exactly once, whatever the delivery order or repetition.

    - succeeded -> +credits (ref payment:<id>:deposit)
    - refunded  -> ensure the deposit is applied first (a refund may overtake its
                   `succeeded`; the late `succeeded` is then a no-op), then -credits.
    A refund may push the balance below zero if the credits were already spent.
    """
    if event.type not in SUPPORTED_TYPES:
        return

    for _ in range(3):
        try:
            db.session.execute(insert(Wallet).values(user_id=event.user_id).on_conflict_do_nothing())
            _post(event.user_id, "deposit", event.credits, f"payment:{event.payment_id}:deposit", +event.credits)
            if event.type == REFUNDED:
                _post(event.user_id, "refund", event.credits, f"payment:{event.payment_id}:refund", -event.credits)
            db.session.commit()
            return
        except IntegrityError:
            # A concurrent delivery inserted the same ref first. Start over: this time
            # the pre-check sees it and only applies what is still missing.
            db.session.rollback()
    raise RuntimeError(f"Could not apply event {event.event_id}")  # 500 -> provider retries
