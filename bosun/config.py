import os

NAMESPACE = os.getenv("BOSUN_NAMESPACE", "bosun")
REVIEW_IMAGE = os.getenv("BOSUN_REVIEW_IMAGE", "ghcr.io/everydaydevopsio/bosun:latest")
REVIEW_PROVIDER = os.getenv("BOSUN_REVIEW_PROVIDER", "codex")
GITHUB_TOKEN_SECRET = os.getenv("BOSUN_GITHUB_TOKEN_SECRET", "bosun-github")
AI_SECRET = os.getenv("BOSUN_AI_SECRET", "bosun-ai")
JOB_TTL_SECONDS = int(os.getenv("BOSUN_JOB_TTL_SECONDS", "3600"))

# The single knob for how long one review may run. The reviewer stops its own
# session at this point; the Job's activeDeadlineSeconds is derived from it with
# a small grace so Kubernetes cannot kill the pod before it reports the failure.
REVIEW_TIMEOUT_SECONDS = int(os.getenv("BOSUN_REVIEW_TIMEOUT_SECONDS", "1800"))
JOB_DEADLINE_GRACE_SECONDS = 120
JOB_DEADLINE_SECONDS = REVIEW_TIMEOUT_SECONDS + JOB_DEADLINE_GRACE_SECONDS

# Maximum reviewer Jobs allowed to run at once. Reviews are rejected rather
# than queued once this is reached, so a busy repository cannot exhaust the
# cluster or the provider's rate limit.
MAX_CONCURRENT_REVIEWS = int(os.getenv("BOSUN_MAX_CONCURRENT_REVIEWS", "3"))
# UID of the "bridge" user baked into the bridgectl base image.
RUN_AS_USER = int(os.getenv("BOSUN_RUN_AS_USER", "1001"))

LOG_FORMAT = os.getenv("BOSUN_LOG_FORMAT", "text")
LOG_LEVEL = os.getenv("BOSUN_LOG_LEVEL", "info")

REVIEW_COMMAND = os.getenv("BOSUN_REVIEW_COMMAND", "@bridgectl review").lower()

# Only these comment authors may spend model credits with the review command.
ALLOWED_ASSOCIATIONS = frozenset(
    value.strip().upper()
    for value in os.getenv("BOSUN_ALLOWED_ASSOCIATIONS", "OWNER,MEMBER,COLLABORATOR").split(",")
    if value.strip()
)

# Reading this lazily keeps the module importable for tests and for the reviewer
# Job, which has no webhook secret and never verifies signatures.
WEBHOOK_SECRET = os.getenv("GITHUB_WEBHOOK_SECRET", "")
