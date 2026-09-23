import os
import pathlib
import shutil
import stat
import subprocess
import tempfile
import requests
from .credentials import materialise
from .github import headers, post_comment
from .githubapp import token_for
from .logs import get_logger
from .session import ReviewFailed, run_review

log = get_logger("bosun.worker")

API = "https://api.github.com"
PROMPT_PATH = pathlib.Path(os.getenv("BOSUN_PROMPT_PATH", "/app/bosun/prompts/code-review.md"))

# Never hand these to the AI provider: it runs against untrusted repository
# content and must not be able to read Bosun's GitHub credential.
SECRET_ENV = (
    "GITHUB_TOKEN", "BOSUN_GIT_TOKEN",
    "BOSUN_GITHUB_PRIVATE_KEY", "BOSUN_GITHUB_PRIVATE_KEY_FILE", "BOSUN_GITHUB_APP_ID",
)

_ASKPASS = """#!/bin/sh
case "$1" in
  Username*) printf '%s\\n' x-access-token ;;
  *) printf '%s\\n' "$BOSUN_GIT_TOKEN" ;;
esac
"""


def run(cmd, cwd=None, env=None):
    return subprocess.run(cmd, cwd=cwd, env=env, text=True, capture_output=True, check=False)


def agent_env() -> dict:
    """Environment for the reviewer agent, with Bosun's credentials removed.

    Provider credentials that live in files are written to disk first and then
    dropped from the environment, so the CLI reads them the way it expects and
    the raw token is not duplicated in the process environment.
    """
    consumed = materialise()
    withheld = set(SECRET_ENV) | set(consumed)
    env = {k: v for k, v in os.environ.items() if k not in withheld}
    env.setdefault("HOME", os.path.expanduser("~"))
    return env


def resolve_pr(repo, number, token):
    r = requests.get(f"{API}/repos/{repo}/pulls/{number}", headers=headers(token), timeout=30)
    r.raise_for_status()
    pr = r.json()
    return pr["head"]["ref"], pr["head"]["sha"]


def _write_askpass(directory: pathlib.Path) -> pathlib.Path:
    path = directory / "askpass.sh"
    path.write_text(_ASKPASS)
    path.chmod(path.stat().st_mode | stat.S_IEXEC)
    return path


def clone(repo: str, ref: str, sha: str, pr_number: int | None, token: str, workspace: pathlib.Path) -> None:
    """Clone `repo` and check out the reviewed commit.

    The token is supplied through GIT_ASKPASS rather than the remote URL so it
    is never written into the workspace's .git/config, where the reviewing
    agent could read it.
    """
    with tempfile.TemporaryDirectory(prefix="bosun-git-") as helper_dir:
        askpass = _write_askpass(pathlib.Path(helper_dir))
        env = dict(os.environ)
        env.update({
            "GIT_ASKPASS": str(askpass),
            "BOSUN_GIT_TOKEN": token,
            "GIT_TERMINAL_PROMPT": "0",
        })

        clone_url = f"https://github.com/{repo}.git"
        result = run(["git", "clone", "--no-tags", clone_url, str(workspace)], env=env)
        if result.returncode:
            raise RuntimeError(_redact(result.stderr, token))

        # A pull request head may live in a fork, which is not reachable as a
        # branch on origin. refs/pull/N/head works for both fork and same-repo PRs.
        fetch_ref = f"refs/pull/{pr_number}/head" if pr_number else ref
        result = run(["git", "fetch", "origin", fetch_ref], cwd=workspace, env=env)
        if result.returncode:
            raise RuntimeError(_redact(result.stderr, token))

        result = run(["git", "checkout", "--detach", sha or "FETCH_HEAD"], cwd=workspace)
        if result.returncode:
            raise RuntimeError(_redact(result.stderr, token))


def _redact(text: str, token: str) -> str:
    return (text or "").replace(token, "***") if token else (text or "")


def build_prompt(repo: str, ref: str, sha: str, local: bool) -> str:
    prompt = PROMPT_PATH.read_text()
    prompt += f"\n\nRepository: {repo}\nReview ref: {ref}\nCommit: {sha or 'working tree'}\n"
    if local:
        prompt += (
            "\nThis is a local development review. Include tracked and uncommitted "
            "working-tree changes in the review. Do not modify them.\n"
        )
    return prompt


def main():
    repo = os.environ["BOSUN_REPO"]
    ref = os.environ["BOSUN_REF"]
    sha = os.getenv("BOSUN_SHA", "")
    pr_number = int(os.environ["BOSUN_PR_NUMBER"]) if os.getenv("BOSUN_PR_NUMBER") else None
    provider = os.getenv("BOSUN_REVIEW_PROVIDER", "codex")
    local_path = os.getenv("BOSUN_LOCAL_PATH")
    # A GitHub App installation token when configured, otherwise GITHUB_TOKEN.
    token = "" if local_path else token_for(repo)

    if pr_number and not local_path and (not sha or ref.startswith("pr-")):
        ref, sha = resolve_pr(repo, pr_number, token)

    cleanup = False
    if local_path:
        workspace = pathlib.Path(local_path)
        if not workspace.is_dir() or not (workspace / ".git").exists():
            raise RuntimeError(f"local review path is not a git repository: {workspace}")
    else:
        if not token:
            raise RuntimeError("GITHUB_TOKEN is required for GitHub-backed reviews")
        workspace = pathlib.Path(tempfile.mkdtemp(prefix="bosun-review-"))
        cleanup = True

    log.info(
        "review starting",
        extra={"context": {"repo": repo, "ref": ref, "sha": sha or "working-tree",
                           "pr": pr_number or "", "provider": provider,
                           "trigger": os.getenv("BOSUN_TRIGGER", "")}},
    )
    try:
        if not local_path:
            clone(repo, ref, sha, pr_number, token, workspace)

        prompt = build_prompt(repo, ref, sha, bool(local_path))
        try:
            output = run_review(workspace, provider, prompt, env=agent_env())
        except ReviewFailed as exc:
            raise RuntimeError(_redact(str(exc), token)) from exc

        if pr_number:
            post_comment(repo, pr_number, "## Bosun code review\n\n" + output[:60000], token)
            log.info("review posted", extra={"context": {"repo": repo, "pr": pr_number}})
        else:
            print(output)
    except Exception as exc:
        log.error("review failed", extra={"context": {"repo": repo, "ref": ref,
                                                      "error": _redact(str(exc), token)[:500]}})
        if pr_number and token:
            try:
                post_comment(repo, pr_number, f"Bosun review failed: `{_redact(str(exc), token)[:1500]}`", token)
            except Exception:
                pass
        raise
    finally:
        if cleanup:
            shutil.rmtree(workspace, ignore_errors=True)


if __name__ == "__main__":
    main()
