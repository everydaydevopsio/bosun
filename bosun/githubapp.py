"""GitHub App authentication.

A long-lived personal access token is the wrong credential for a service that
runs untrusted code: it cannot be scoped per repository at runtime, it does not
expire, and revoking it breaks every installation at once.

A GitHub App instead signs a short-lived JWT with its private key, exchanges it
for an installation token scoped to one installation, and that token expires
after an hour. Bosun mints one per review.

Set BOSUN_GITHUB_APP_ID and BOSUN_GITHUB_PRIVATE_KEY (PEM) to enable this; the
`token` key of the GitHub secret remains the fallback.
"""

import os
import time

import jwt
import requests

from .github import API, headers
from .logs import get_logger

log = get_logger("bosun.githubapp")

# GitHub rejects JWTs older than 10 minutes and allows 60s of clock drift.
JWT_TTL_SECONDS = 540
CLOCK_SKEW_SECONDS = 60


class GitHubAppError(RuntimeError):
    pass


def configured() -> bool:
    return bool(os.getenv("BOSUN_GITHUB_APP_ID") and _private_key())


def _private_key() -> str:
    key = os.getenv("BOSUN_GITHUB_PRIVATE_KEY", "")
    if key:
        # Kubernetes Secrets frequently carry the PEM with literal \n.
        return key.replace("\\n", "\n")
    path = os.getenv("BOSUN_GITHUB_PRIVATE_KEY_FILE", "")
    if path and os.path.exists(path):
        return open(path).read()
    return ""


def app_jwt(app_id: str | None = None, private_key: str | None = None) -> str:
    """Sign the short-lived JWT that identifies the App itself."""
    app_id = app_id or os.getenv("BOSUN_GITHUB_APP_ID", "")
    private_key = private_key or _private_key()
    if not app_id or not private_key:
        raise GitHubAppError("BOSUN_GITHUB_APP_ID and BOSUN_GITHUB_PRIVATE_KEY are required")
    now = int(time.time())
    payload = {"iat": now - CLOCK_SKEW_SECONDS, "exp": now + JWT_TTL_SECONDS, "iss": app_id}
    try:
        return jwt.encode(payload, private_key, algorithm="RS256")
    except Exception as exc:  # noqa: BLE001 - surface key problems clearly
        raise GitHubAppError(f"could not sign App JWT (is the PEM valid?): {exc}") from exc


def installation_id(repo: str, bearer: str) -> int:
    """Find the installation that covers `repo` (owner/name)."""
    owner, _, name = repo.partition("/")
    r = requests.get(
        f"{API}/repos/{owner}/{name}/installation",
        headers={**headers(bearer), "Authorization": f"Bearer {bearer}"},
        timeout=30,
    )
    if r.status_code == 404:
        raise GitHubAppError(f"the GitHub App is not installed on {repo}")
    r.raise_for_status()
    return r.json()["id"]


def installation_token(repo: str) -> str:
    """Mint an installation token scoped to the repository under review."""
    bearer = app_jwt()
    install = installation_id(repo, bearer)
    r = requests.post(
        f"{API}/app/installations/{install}/access_tokens",
        headers={**headers(bearer), "Authorization": f"Bearer {bearer}"},
        json={"repositories": [repo.partition("/")[2]],
              "permissions": {"contents": "read", "pull_requests": "write", "checks": "write"}},
        timeout=30,
    )
    if r.status_code not in (200, 201):
        raise GitHubAppError(f"could not mint installation token: {r.status_code} {r.text[:200]}")
    body = r.json()
    log.info(
        "minted installation token",
        extra={"context": {"repo": repo, "installation": install, "expires": body.get("expires_at", "")}},
    )
    return body["token"]


def token_for(repo: str) -> str:
    """Return the credential to use for `repo`.

    Prefers a short-lived App installation token and falls back to GITHUB_TOKEN
    so an existing PAT installation keeps working.
    """
    if configured():
        try:
            return installation_token(repo)
        except (GitHubAppError, requests.RequestException) as exc:
            fallback = os.getenv("GITHUB_TOKEN", "")
            if not fallback:
                raise
            log.warning(
                "falling back to GITHUB_TOKEN",
                extra={"context": {"repo": repo, "reason": str(exc)[:200]}},
            )
            return fallback
    return os.getenv("GITHUB_TOKEN", "")
