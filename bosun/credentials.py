"""Materialise provider credential files from the environment.

Some agent CLIs authenticate from a file rather than an environment variable.
`codex` in ChatGPT OAuth mode reads `~/.codex/auth.json`, which holds refresh
and access tokens rather than an API key, so there is no `OPENAI_API_KEY` to
pass through.

Kubernetes Secrets carry these as single env vars (the AWS secret behind
`scripts/with-secrets.sh` exposes `CODEX_AUTH`). This module writes them back
to the paths the CLIs expect, with restrictive permissions, before the bridge
server starts the provider.
"""

import json
import os
import pathlib

from .logs import get_logger

log = get_logger("bosun.credentials")

# env var -> path under $HOME that the provider CLI reads.
CREDENTIAL_FILES = {
    "CODEX_AUTH": ".codex/auth.json",
    "CLAUDE_CREDENTIALS": ".claude/.credentials.json",
    "GEMINI_OAUTH_CREDS": ".gemini/oauth_creds.json",
}


def materialise(home: str | None = None, environ: dict | None = None) -> list[str]:
    """Write any credential files present in the environment.

    Returns the env var names that were consumed, so the caller can keep them
    out of the provider's environment once the file exists.
    """
    environ = os.environ if environ is None else environ
    base = pathlib.Path(home or environ.get("HOME") or os.path.expanduser("~"))
    written = []

    for variable, relative in CREDENTIAL_FILES.items():
        value = environ.get(variable)
        if not value:
            continue
        target = base / relative
        try:
            target.parent.mkdir(parents=True, exist_ok=True)
            target.parent.chmod(0o700)
            target.write_text(_normalise(value))
            target.chmod(0o600)
        except OSError as exc:
            log.warning(
                "could not write provider credential",
                extra={"context": {"variable": variable, "path": str(target), "error": str(exc)}},
            )
            continue
        written.append(variable)
        log.info(
            "wrote provider credential",
            extra={"context": {"variable": variable, "path": str(target)}},
        )
    return written


def _normalise(value: str) -> str:
    """Keep JSON credentials valid even if the secret was stored oddly."""
    text = value.strip()
    try:
        # Re-serialising proves it parses and strips any wrapping whitespace.
        return json.dumps(json.loads(text))
    except ValueError:
        return text
