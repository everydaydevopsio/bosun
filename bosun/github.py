import hashlib
import hmac
import os
import requests

API = "https://api.github.com"

def verify_signature(secret: str, body: bytes, signature: str | None) -> bool:
    if not signature or not signature.startswith("sha256="):
        return False
    expected = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, signature)

def headers(token: str) -> dict[str, str]:
    return {
        "Authorization": f"Bearer {token}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }

def post_comment(repo: str, number: int, body: str, token: str) -> None:
    r = requests.post(f"{API}/repos/{repo}/issues/{number}/comments", headers=headers(token), json={"body": body}, timeout=30)
    r.raise_for_status()
