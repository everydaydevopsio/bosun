import re
from kubernetes import client, config
from . import config as settings

def _name(repo: str, ref: str, delivery: str) -> str:
    raw = f"review-{repo.split('/')[-1]}-{ref}-{delivery[:8]}".lower()
    return re.sub(r"[^a-z0-9-]+", "-", raw).strip("-")[:63].rstrip("-")

def submit_review(repo: str, ref: str, sha: str, delivery: str, pr_number: int | None, trigger: str) -> str:
    try:
        config.load_incluster_config()
    except config.ConfigException:
        config.load_kube_config()

    name = _name(repo, ref, delivery)
    env = [
        client.V1EnvVar(name="BOSUN_REPO", value=repo),
        client.V1EnvVar(name="BOSUN_REF", value=ref),
        client.V1EnvVar(name="BOSUN_SHA", value=sha),
        client.V1EnvVar(name="BOSUN_TRIGGER", value=trigger),
        client.V1EnvVar(name="BOSUN_REVIEW_PROVIDER", value=settings.REVIEW_PROVIDER),
        client.V1EnvVar(name="BOSUN_PR_NUMBER", value=str(pr_number or "")),
        client.V1EnvVar(name="GITHUB_TOKEN", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.GITHUB_TOKEN_SECRET, key="token"))),
        client.V1EnvVar(name="OPENAI_API_KEY", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.AI_SECRET, key="openai-api-key", optional=True))),
        client.V1EnvVar(name="CLAUDE_CODE_OAUTH_TOKEN", value_from=client.V1EnvVarSource(secret_key_ref=client.V1SecretKeySelector(name=settings.AI_SECRET, key="claude-code-oauth-token", optional=True))),
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
            capabilities=client.V1Capabilities(drop=["ALL"]),
        ),
    )
    pod = client.V1PodTemplateSpec(
        metadata=client.V1ObjectMeta(labels={"app.kubernetes.io/name": "bosun-review", "bosun/repository": repo.replace("/", "-")[:63]}),
        spec=client.V1PodSpec(restart_policy="Never", containers=[container], automount_service_account_token=False),
    )
    job = client.V1Job(
        metadata=client.V1ObjectMeta(name=name, labels={"app.kubernetes.io/managed-by": "bosun"}),
        spec=client.V1JobSpec(template=pod, backoff_limit=0, ttl_seconds_after_finished=settings.JOB_TTL_SECONDS, active_deadline_seconds=1800),
    )
    client.BatchV1Api().create_namespaced_job(settings.NAMESPACE, job)
    return name
