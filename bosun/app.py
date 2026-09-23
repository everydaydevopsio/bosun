import json
from fastapi import FastAPI, Header, HTTPException, Request
from starlette.concurrency import run_in_threadpool
from .config import ALLOWED_ASSOCIATIONS, MAX_CONCURRENT_REVIEWS, REVIEW_COMMAND, WEBHOOK_SECRET
from .github import verify_signature
from .jobs import AtCapacity, JobExists, submit_review
from .logs import get_logger

log = get_logger("bosun.app")

app = FastAPI(title="Bosun", version="0.1.0")

PR_ACTIONS = {"opened", "reopened", "synchronize", "ready_for_review"}

log.info(
    "bosun starting",
    extra={"context": {"max_concurrent_reviews": MAX_CONCURRENT_REVIEWS,
                       "allowed_associations": ",".join(sorted(ALLOWED_ASSOCIATIONS))}},
)

if not WEBHOOK_SECRET:
    log.warning(
        "GITHUB_WEBHOOK_SECRET is not set; every webhook delivery will be rejected. "
        "This is expected in development mode, where reviews are launched locally."
    )


@app.get("/healthz")
def healthz():
    return {"ok": True}


def _is_bot(payload: dict) -> bool:
    """Bosun's own review comments must never trigger another review."""
    return (payload.get("sender") or {}).get("type") == "Bot"


def _comment_authorized(payload: dict) -> bool:
    return comment_association(payload) in ALLOWED_ASSOCIATIONS


def comment_association(payload: dict) -> str | None:
    return (payload.get("comment") or {}).get("author_association")


def review_request(event: str, payload: dict):
    repo = payload.get("repository", {}).get("full_name")
    if not repo:
        return None

    if event == "create" and payload.get("ref_type") == "branch":
        # The `create` event carries no commit SHA (`master_branch` is the
        # default branch name, not the new branch's head), so leave the SHA
        # empty and let the worker check out FETCH_HEAD of the created ref.
        return repo, payload["ref"], "", None, "branch-created"

    if event == "pull_request" and payload.get("action") in PR_ACTIONS:
        pr = payload["pull_request"]
        return repo, pr["head"]["ref"], pr["head"]["sha"], pr["number"], f"pull-request-{payload['action']}"

    if event in {"issue_comment", "pull_request_review_comment"} and payload.get("action") == "created":
        if _is_bot(payload):
            return None
        body = payload.get("comment", {}).get("body", "")
        if REVIEW_COMMAND not in body.lower():
            return None
        association = comment_association(payload)
        if not _comment_authorized(payload):
            log.warning(
                "ignoring review command from unauthorized author",
                extra={"context": {"repo": repo, "association": association or "none"}},
            )
            return None
        issue = payload.get("issue", {})
        # issue_comment fires for plain issues too; only pull requests are reviewable.
        if event == "issue_comment" and not issue.get("pull_request"):
            return None
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
    try:
        payload = json.loads(body)
    except ValueError:
        raise HTTPException(status_code=400, detail="payload is not valid JSON")
    if not isinstance(payload, dict):
        raise HTTPException(status_code=400, detail="payload is not a JSON object")

    req = review_request(x_github_event or "", payload)
    if not req:
        return {"accepted": False, "reason": "event does not request a review"}
    repo, ref, sha, pr_number, trigger = req
    delivery = x_github_delivery or "manual"
    try:
        # The Kubernetes client is blocking; keep it off the event loop so a
        # burst of deliveries cannot stall GitHub's 10s webhook timeout.
        job = await run_in_threadpool(submit_review, repo, ref, sha, delivery, pr_number, trigger)
    except JobExists:
        # GitHub retries deliveries; the same delivery must not queue twice.
        log.info("duplicate delivery ignored", extra={"context": {"repo": repo, "delivery": delivery}})
        return {"accepted": True, "duplicate": True, "delivery": delivery}
    except AtCapacity as exc:
        # Reject rather than queue: GitHub shows the delivery as failed and it
        # can be redelivered once a slot frees up.
        raise HTTPException(
            status_code=503,
            detail=f"{exc}; retry when a review slot frees up",
            headers={"Retry-After": "120"},
        ) from exc
    log.info(
        "review accepted",
        extra={"context": {"repo": repo, "ref": ref, "trigger": trigger, "job": job, "delivery": delivery}},
    )
    return {"accepted": True, "job": job}
