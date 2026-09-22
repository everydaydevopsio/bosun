import os

NAMESPACE = os.getenv("BOSUN_NAMESPACE", "bosun")
REVIEW_IMAGE = os.getenv("BOSUN_REVIEW_IMAGE", "ghcr.io/everydaydevopsio/bosun:latest")
REVIEW_PROVIDER = os.getenv("BOSUN_REVIEW_PROVIDER", "codex")
WEBHOOK_SECRET = os.environ["GITHUB_WEBHOOK_SECRET"]
GITHUB_TOKEN_SECRET = os.getenv("BOSUN_GITHUB_TOKEN_SECRET", "bosun-github")
AI_SECRET = os.getenv("BOSUN_AI_SECRET", "bosun-ai")
JOB_TTL_SECONDS = int(os.getenv("BOSUN_JOB_TTL_SECONDS", "3600"))
