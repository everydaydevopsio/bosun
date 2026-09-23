"""Structured logging, shaped like bridgectl's.

bridgectl configures Go's ``slog`` with a text or JSON handler on stderr and a
level from a flag. Bosun mirrors that so both halves of a review read the same
way in `kubectl logs`:

    BOSUN_LOG_FORMAT=json|text   (default text)
    BOSUN_LOG_LEVEL=debug|info|warning|error   (default info)

Use ``log.info("message", extra={"context": {...}})`` to attach structured
fields; they render as ``key=value`` pairs in text mode and as JSON members in
JSON mode.
"""

import json
import logging
import os
import sys
import time

_CONFIGURED = False

# Attributes LogRecord always carries; anything else was added by the caller.
_STANDARD = frozenset(
    logging.LogRecord("", 0, "", 0, "", None, None).__dict__
) | {"message", "asctime", "taskName", "context"}


def _fields(record: logging.LogRecord) -> dict:
    fields = dict(getattr(record, "context", None) or {})
    for key, value in record.__dict__.items():
        if key not in _STANDARD and not key.startswith("_"):
            fields[key] = value
    return fields


class TextFormatter(logging.Formatter):
    """`time=... level=INFO msg="..." key=value` — slog's text handler."""

    def format(self, record: logging.LogRecord) -> str:
        stamp = time.strftime("%Y-%m-%dT%H:%M:%S%z", time.localtime(record.created))
        parts = [
            f"time={stamp}",
            f"level={record.levelname}",
            f"msg={json.dumps(record.getMessage())}",
            f"logger={record.name}",
        ]
        for key, value in _fields(record).items():
            parts.append(f"{key}={value if isinstance(value, (int, float)) else json.dumps(str(value))}")
        line = " ".join(parts)
        if record.exc_info:
            line += "\n" + self.formatException(record.exc_info)
        return line


class JSONFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        payload = {
            "time": time.strftime("%Y-%m-%dT%H:%M:%S%z", time.localtime(record.created)),
            "level": record.levelname,
            "msg": record.getMessage(),
            "logger": record.name,
        }
        payload.update({k: _jsonable(v) for k, v in _fields(record).items()})
        if record.exc_info:
            payload["error"] = self.formatException(record.exc_info)
        return json.dumps(payload)


def _jsonable(value):
    if isinstance(value, (str, int, float, bool)) or value is None:
        return value
    return str(value)


def configure(force: bool = False) -> None:
    """Install the root handler. Safe to call more than once."""
    global _CONFIGURED
    if _CONFIGURED and not force:
        return
    level_name = os.getenv("BOSUN_LOG_LEVEL", "info").upper()
    level = getattr(logging, level_name, logging.INFO)
    formatter = JSONFormatter() if os.getenv("BOSUN_LOG_FORMAT", "text").lower() == "json" else TextFormatter()

    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(formatter)

    root = logging.getLogger()
    for existing in list(root.handlers):
        root.removeHandler(existing)
    root.addHandler(handler)
    root.setLevel(level)

    # Library chatter drowns out Bosun's own lines in `kubectl logs`.
    for noisy in ("httpx", "httpcore", "urllib3", "kubernetes", "grpc"):
        logging.getLogger(noisy).setLevel(max(level, logging.WARNING))
    _CONFIGURED = True


def get_logger(name: str) -> logging.Logger:
    configure()
    return logging.getLogger(name)
