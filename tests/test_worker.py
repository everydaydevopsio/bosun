import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from bosun import worker


def git(*args, cwd=None):
    subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True)


class AgentEnvTests(unittest.TestCase):
    def test_github_token_is_withheld_from_the_agent(self):
        """The reviewer agent reads untrusted code; it must not see the token."""
        with mock.patch.dict(os.environ, {"GITHUB_TOKEN": "ghp_secret", "OPENAI_API_KEY": "sk-x"}):
            env = worker.agent_env()
        self.assertNotIn("GITHUB_TOKEN", env)
        self.assertEqual(env["OPENAI_API_KEY"], "sk-x")


class RedactTests(unittest.TestCase):
    def test_redacts_token_from_messages(self):
        self.assertEqual(worker._redact("fatal: ghp_secret bad", "ghp_secret"), "fatal: *** bad")

    def test_no_token_is_a_passthrough(self):
        self.assertEqual(worker._redact("fatal", ""), "fatal")


class CloneTests(unittest.TestCase):
    """The clone must not leave the credential inside the reviewed workspace."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.origin = Path(self.tmp.name) / "origin"
        self.origin.mkdir()
        git("init", "-q", "--initial-branch=main", ".", cwd=self.origin)
        (self.origin / "a.txt").write_text("hello\n")
        git("add", "-A", cwd=self.origin)
        git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init", cwd=self.origin)

    def test_token_is_not_written_to_git_config(self):
        workspace = Path(self.tmp.name) / "ws"
        with mock.patch.object(worker, "run", wraps=worker.run) as runner:
            # Point the clone at the local origin instead of github.com.
            with mock.patch.object(worker, "clone", worker.clone):
                calls = []

                def fake_run(cmd, cwd=None, env=None):
                    if cmd[:2] == ["git", "clone"]:
                        cmd = ["git", "clone", "--no-tags", str(self.origin), str(workspace)]
                    if cmd[:2] == ["git", "fetch"]:
                        cmd = ["git", "fetch", "origin", "main"]
                    calls.append(cmd)
                    return subprocess.run(cmd, cwd=cwd, env=env, text=True, capture_output=True)

                runner.side_effect = fake_run
                worker.clone("acme/widget", "main", "", None, "ghp_secret", workspace)

        config = (workspace / ".git" / "config").read_text()
        self.assertNotIn("ghp_secret", config)
        self.assertTrue((workspace / "a.txt").exists())

    def test_pull_request_fetches_the_pull_ref(self):
        """Fork PR heads only exist under refs/pull/N/head on the base repo."""
        recorded = []

        def fake_run(cmd, cwd=None, env=None):
            recorded.append(cmd)
            return subprocess.CompletedProcess(cmd, 0, "", "")

        with mock.patch.object(worker, "run", side_effect=fake_run):
            worker.clone("acme/widget", "feature/x", "abc", 42, "t", Path("/tmp/ws"))

        fetches = [c for c in recorded if c[:2] == ["git", "fetch"]]
        self.assertEqual(fetches[0][-1], "refs/pull/42/head")

    def test_branch_review_fetches_the_branch(self):
        recorded = []

        def fake_run(cmd, cwd=None, env=None):
            recorded.append(cmd)
            return subprocess.CompletedProcess(cmd, 0, "", "")

        with mock.patch.object(worker, "run", side_effect=fake_run):
            worker.clone("acme/widget", "feature/x", "", None, "t", Path("/tmp/ws"))

        fetches = [c for c in recorded if c[:2] == ["git", "fetch"]]
        self.assertEqual(fetches[0][-1], "feature/x")
        checkouts = [c for c in recorded if c[:2] == ["git", "checkout"]]
        self.assertEqual(checkouts[0][-1], "FETCH_HEAD")

    def test_clone_url_has_no_credentials(self):
        recorded = []

        def fake_run(cmd, cwd=None, env=None):
            recorded.append(cmd)
            return subprocess.CompletedProcess(cmd, 0, "", "")

        with mock.patch.object(worker, "run", side_effect=fake_run):
            worker.clone("acme/widget", "main", "", None, "ghp_secret", Path("/tmp/ws"))

        clone_cmd = [c for c in recorded if c[:2] == ["git", "clone"]][0]
        self.assertIn("https://github.com/acme/widget.git", clone_cmd)
        self.assertFalse(any("ghp_secret" in part for part in clone_cmd))


class PromptTests(unittest.TestCase):
    def test_local_reviews_mention_uncommitted_changes(self):
        with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as fh:
            fh.write("BASE PROMPT")
            path = Path(fh.name)
        self.addCleanup(os.unlink, path)
        with mock.patch.object(worker, "PROMPT_PATH", path):
            local = worker.build_prompt("demo", "main", "", True)
            remote = worker.build_prompt("acme/widget", "main", "abc123", False)
        self.assertIn("uncommitted", local)
        self.assertNotIn("uncommitted", remote)
        self.assertIn("abc123", remote)


if __name__ == "__main__":
    unittest.main()
