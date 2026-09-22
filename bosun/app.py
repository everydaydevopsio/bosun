import json
import os
from fastapi import FastAPI, Header, HTTPException, Request
from .config import WEBHOOK_SECRET
from .github import verify_signature
from .jobs import submit_review

app = FastAPI(title="Bosun", version="0.1.0")

@app.get("/healthz")
def healthz():
    return {"ok": True}

def review_request(event: str, payload: dict):
    repo = payload.get("repository", {}).get("full_name")
    if not repo:
        return None

    if event == "create" and payload.get("ref_type") == "branch":
        return repo, payload["ref"], payload.get("after") or payload.get("master_branch", ""), None, "branch-created"

    if event == "pull_request" and payload.get("action") in {"opened", "reopened", "synchronize", "ready_for_review"}:
        pr = payload["pull_request"]
        return repo, pr["head"]["ref"], pr["head"]["sha"], pr["number"], f"pull-request-{payload['action']}"

    if event in {"issue_comment", "pull_request_review_comment"} and payload.get("action") == "created":
        body = payload.get("comment", {}).get("body", "")
        if "@bridgectl review" not in body.lower():
            return None
        issue = payload.get("issue", {})
        pr_number = issue.get("number") or payload.get("pull_request", {}).get("number")
        if not pr_number:
            return None
        # The worker resolves the PR head ref/SHA with the GitHub token.
        return repo, f"pr-{pr_number}", "", pr_number, "comment"

    return None

@app.post("/webhooks/github")
async def github_webhook(
    request: Request,
    x_hub_signature_256: str | None = Header(default=None),
    x_github_event: str | None = Header(default=None),
    x_github_delivery: str | None = Header(default=None),
):
    body = await request.body()
    if not verify_signature(WEBHOOK_SECRET, body, x_hub_signature_256):
        raise HTTPException(status_code=401, detail="invalid webhook signature")
    payload = json.loads(body)
    req = review_request(x_github_event or "", payload)
    if not req:
        return {"accepted": False, "reason": "event does not request a review"}
    repo, ref, sha, pr_number, trigger = req
    job = submit_review(repo, ref, sha, x_github_delivery or "manual", pr_number, trigger)
    return {"accepted": True, "job": job}
