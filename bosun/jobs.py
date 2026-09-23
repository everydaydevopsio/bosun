import hashlib
import re
from kubernetes import client, config
from . import config as settings
from .logs import get_logger

log = get_logger("bosun.jobs")

MAX_NAME = 63


class JobExists(Exception):
    """A Job for this delivery already exists (GitHub retried the webhook)."""


class AtCapacity(Exception):
    """Too many reviews are already running."""


def active_reviews(batch=None) -> int:
    """Count reviewer Jobs that are neither complete nor failed."""
    batch = batch or client.BatchV1Api()
    jobs = batch.list_namespaced_job(
        settings.NAMESPACE, label_selector="app.kubernetes.io/managed-by=bosun"
    )
    running = 0
    for job in jobs.items:
        status = job.status
        if status is None:
            running += 1
            continue
        finished = (status.succeeded or 0) or (status.failed or 0)
        if not finished:
            running += 1
    return running


def _name(repo: str, ref: str, delivery: str) -> str:
    """Build a DNS-safe, collision-free Job name.

    The delivery suffix is appended after truncation so that long branch names
    cannot push it off the end and make two different deliveries collide.
    """
    suffix = hashlib.sha256(f"{repo}\n{ref}\n{delivery}".encode()).hexdigest()[:10]
    raw = f"review-{repo.split('/')[-1]}-{ref}".lower()
    stem = re.sub(r"[^a-z0-9-]+", "-", raw).strip("-")
    stem = stem[: MAX_NAME - len(suffix) - 1].strip("-")
    return f"{stem}-{suffix}" if stem else f"review-{suffix}"


def submit_review(repo: str, ref: str, sha: str, delivery: str, pr_number: int | None, trigger: str) -> str:
    try:
        config.load_incluster_config()
    except config.ConfigException:
        config.load_kube_config()

    batch = client.BatchV1Api()
    running = active_reviews(batch)
    if running >= settings.MAX_CONCURRENT_REVIEWS:
        log.warning(
            "rejecting review: at capacity",
            extra={"context": {"repo": repo, "running": running,
                               "cap": settings.MAX_CONCURRENT_REVIEWS, "delivery": delivery}},
        )
        raise AtCapacity(f"{running} reviews already running (cap {settings.MAX_CONCURRENT_REVIEWS})")

    name = _name(repo, ref, delivery)
    env = [
        client.V1EnvVar(name="BOSUN_REPO", value=repo),
        client.V1EnvVar(name="BOSUN_REF", value=ref),
        client.V1EnvVar(name="BOSUN_SHA", value=sha),
        client.V1EnvVar(name="BOSUN_TRIGGER", value=trigger),
        client.V1EnvVar(name="BOSUN_REVIEW_PROVIDER", value=settings.REVIEW_PROVIDER),
        client.V1EnvVar(name="BOSUN_REVIEW_TIMEOUT_SECONDS", value=str(settings.REVIEW_TIMEOUT_SECONDS)),
        client.V1EnvVar(name="BOSUN_PR_NUMBER", value=str(pr_number or "")),
        client.V1EnvVar(name="GITHUB_TOKEN", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.GITHUB_TOKEN_SECRET, key="token", optional=True))),
        client.V1EnvVar(name="OPENAI_API_KEY", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.AI_SECRET, key="openai-api-key", optional=True))),
        client.V1EnvVar(name="CLAUDE_CODE_OAUTH_TOKEN", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.AI_SECRET, key="claude-code-oauth-token", optional=True))),
        client.V1EnvVar(name="ANTHROPIC_API_KEY", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.AI_SECRET, key="anthropic-api-key", optional=True))),
        client.V1EnvVar(name="GEMINI_API_KEY", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.AI_SECRET, key="gemini-api-key", optional=True))),
        # File-based provider credentials; the reviewer writes these to disk.
        client.V1EnvVar(name="CODEX_AUTH", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.AI_SECRET, key="codex-auth", optional=True))),
        client.V1EnvVar(name="CLAUDE_CREDENTIALS", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.AI_SECRET, key="claude-credentials", optional=True))),
        client.V1EnvVar(name="BOSUN_GITHUB_APP_ID", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.GITHUB_TOKEN_SECRET, key="app-id", optional=True))),
        client.V1EnvVar(name="BOSUN_GITHUB_PRIVATE_KEY", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.GITHUB_TOKEN_SECRET, key="private-key", optional=True))),
        client.V1EnvVar(name="BOSUN_LOG_FORMAT", value=settings.LOG_FORMAT),
        client.V1EnvVar(name="BOSUN_LOG_LEVEL", value=settings.LOG_LEVEL),
    ]
    container = client.V1Container(
        name="reviewer", image=settings.REVIEW_IMAGE,
        command=["python3", "-m", "bosun.worker"], env=env,
        resources=client.V1ResourceRequirements(
            requests={"cpu": "250m", "memory": "512Mi"},
            limits={"cpu": "2", "memory": "2Gi"},
        ),
        security_context=client.V1SecurityContext(
            allow_privilege_escalation=False,
            run_as_non_root=True,
            run_as_user=settings.RUN_AS_USER,
            capabilities=client.V1Capabilities(drop=["ALL"]),
        ),
    )
    pod = client.V1PodTemplateSpec(
        metadata=client.V1ObjectMeta(labels={"app.kubernetes.io/name": "bosun-review", "bosun/repository": repo.replace("/", "-")[:63]}),
        spec=client.V1PodSpec(restart_policy="Never", containers=[container], automount_service_account_token=False),
    )
    job = client.V1Job(
        metadata=client.V1ObjectMeta(name=name, labels={"app.kubernetes.io/managed-by": "bosun"}),
        spec=client.V1JobSpec(
            template=pod, backoff_limit=0,
            ttl_seconds_after_finished=settings.JOB_TTL_SECONDS,
            active_deadline_seconds=settings.JOB_DEADLINE_SECONDS,
        ),
    )
    try:
        batch.create_namespaced_job(settings.NAMESPACE, job)
    except client.ApiException as exc:
        if exc.status == 409:
            raise JobExists(name) from exc
        raise
    log.info(
        "review job created",
        extra={"context": {"job": name, "repo": repo, "ref": ref, "trigger": trigger,
                           "pr": pr_number or "", "running": running + 1}},
    )
    return name
