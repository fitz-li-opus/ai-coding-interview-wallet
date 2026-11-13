import os


class Config:
    SQLALCHEMY_DATABASE_URI = os.getenv("DATABASE_URL", "sqlite:///wallet.db")
    # Signing secret shared with the payment provider.
    WEBHOOK_SECRET = os.getenv("WEBHOOK_SECRET", "whsec_interview_test")
    # The provider re-signs every delivery attempt, so a short window is safe.
    WEBHOOK_TOLERANCE_SECONDS = 300
    # SQLite allows one writer at a time; concurrent requests queue on the write lock
    # instead of failing with "database is locked".
    SQLALCHEMY_ENGINE_OPTIONS = {"connect_args": {"timeout": 30}}


class TestConfig(Config):
    TESTING = True
    SQLALCHEMY_DATABASE_URI = "sqlite:///:memory:"
    WEBHOOK_SECRET = "whsec_test"
