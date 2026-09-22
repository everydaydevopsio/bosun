import os
import pathlib
import shutil
import subprocess
import tempfile
import requests
from .github import headers, post_comment

API = "https://api.github.com"

def run(cmd, cwd=None, input_text=None):
    return subprocess.run(cmd, cwd=cwd, input=input_text, text=True, capture_output=True, check=False)

def resolve_pr(repo, number, token):
    r = requests.get(f"{API}/repos/{repo}/pulls/{number}", headers=headers(token), timeout=30)
    r.raise_for_status()
    pr = r.json()
    return pr["head"]["ref"], pr["head"]["sha"]

def main():
    repo = os.environ["BOSUN_REPO"]
    ref = os.environ["BOSUN_REF"]
    sha = os.getenv("BOSUN_SHA", "")
    pr_number = int(os.environ["BOSUN_PR_NUMBER"]) if os.getenv("BOSUN_PR_NUMBER") else None
    provider = os.getenv("BOSUN_REVIEW_PROVIDER", "codex")
    local_path = os.getenv("BOSUN_LOCAL_PATH")
    token = os.getenv("GITHUB_TOKEN", "")

    if pr_number and (not sha or ref.startswith("pr-")):
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

    try:
        if not local_path:
            clone_url = f"https://x-access-token:{token}@github.com/{repo}.git"
            result = run(["git", "clone", "--no-tags", clone_url, str(workspace)])
            if result.returncode:
                raise RuntimeError(result.stderr)
            result = run(["git", "fetch", "origin", ref], cwd=workspace)
            if result.returncode:
                raise RuntimeError(result.stderr)
            result = run(["git", "checkout", "--detach", sha or "FETCH_HEAD"], cwd=workspace)
            if result.returncode:
                raise RuntimeError(result.stderr)

        prompt = pathlib.Path("/app/bosun/prompts/code-review.md").read_text()
        prompt += f"\n\nRepository: {repo}\nReview ref: {ref}\nCommit: {sha or 'working tree'}\n"
        if local_path:
            prompt += "\nThis is a local development review. Include tracked and uncommitted working-tree changes in the review. Do not modify them.\n"

        review = run(
            ["bridgectl", "run", "--no-tty", "--provider", provider, "--project", "bosun-review", str(workspace)],
            cwd="/app", input_text=prompt,
        )
        output = (review.stdout or "").strip()
        if review.returncode:
            raise RuntimeError((review.stderr or output or "reviewer failed")[-4000:])
        if not output:
            output = "Bosun reviewer completed without producing review text."

        if pr_number:
            post_comment(repo, pr_number, "## Bosun code review\n\n" + output[:60000], token)
        else:
            print(output)
    except Exception as exc:
        if pr_number and token:
            try:
                post_comment(repo, pr_number, f"Bosun review failed: `{str(exc)[:1500]}`", token)
            except Exception:
                pass
        raise
    finally:
        if cleanup:
            shutil.rmtree(workspace, ignore_errors=True)

if __name__ == "__main__":
    main()
