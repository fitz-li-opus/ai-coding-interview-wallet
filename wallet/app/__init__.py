from flask import Flask
from sqlalchemy import event

from .config import Config
from .errors import register_error_handlers
from .extensions import db


def create_app(config_object=Config) -> Flask:
    app = Flask(__name__)
    app.config.from_object(config_object)

    db.init_app(app)

    from . import models  # noqa: F401  (register tables)
    from .routes import bp

    app.register_blueprint(bp)
    register_error_handlers(app)

    with app.app_context():
        # WAL: readers (GET /wallets) never block on, or get blocked by, the single writer.
        @event.listens_for(db.engine, "connect")
        def _sqlite_pragmas(dbapi_connection, _record):
            cursor = dbapi_connection.cursor()
            cursor.execute("PRAGMA journal_mode=WAL")
            cursor.close()

        db.create_all()
    return app
