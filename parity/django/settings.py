"""Settings of the parity project: two apps, one PostgreSQL database.

The database is the throw-away one itest/internal/dbtest hands out, passed
in as PARITY_DSN; the gormgate side of the harness reads a PARITY_DSN of
its own. Both sides run on the same PostgreSQL server so that the SQL they
print is comparable statement for statement (docs/N_A.md).
"""
import os
from urllib.parse import parse_qsl, unquote, urlparse

SECRET_KEY = "parity"
DEBUG = True
INSTALLED_APPS = ["auth_app", "blog"]

_dsn = urlparse(os.environ["PARITY_DSN"])
DATABASES = {
    "default": {
        "ENGINE": "django.db.backends.postgresql",
        "NAME": _dsn.path.lstrip("/"),
        "USER": unquote(_dsn.username or ""),
        "PASSWORD": unquote(_dsn.password or ""),
        "HOST": _dsn.hostname or "",
        "PORT": str(_dsn.port or ""),
        # The remaining DSN parameters (sslmode, connect_timeout) are
        # libpq keywords, which psycopg takes as connection arguments.
        "OPTIONS": dict(parse_qsl(_dsn.query)),
    }
}

if os.environ.get("PARITY_DISABLE_MIGRATIONS"):
    MIGRATION_MODULES = {app: None for app in os.environ["PARITY_DISABLE_MIGRATIONS"].split(",")}

USE_TZ = True
DEFAULT_AUTO_FIELD = "django.db.models.BigAutoField"
