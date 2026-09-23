import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from bosun import credentials, worker

AUTH = json.dumps({"OPENAI_API_KEY": None, "auth_mode": "chatgpt",
                   "tokens": {"access_token": "a", "refresh_token": "r"}})


class MaterialiseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.home = Path(self.tmp.name)

    def test_codex_auth_is_written_where_the_cli_reads_it(self):
        written = credentials.materialise(home=self.home, environ={"CODEX_AUTH": AUTH})
        target = self.home / ".codex" / "auth.json"
        self.assertEqual(written, ["CODEX_AUTH"])
        self.assertTrue(target.exists())
        self.assertEqual(json.loads(target.read_text())["auth_mode"], "chatgpt")

    def test_credential_file_is_not_world_readable(self):
        credentials.materialise(home=self.home, environ={"CODEX_AUTH": AUTH})
        target = self.home / ".codex" / "auth.json"
        self.assertEqual(target.stat().st_mode & 0o077, 0)
        self.assertEqual(target.parent.stat().st_mode & 0o077, 0)

    def test_nothing_written_without_credentials(self):
        self.assertEqual(credentials.materialise(home=self.home, environ={}), [])
        self.assertFalse((self.home / ".codex").exists())

    def test_empty_value_is_ignored(self):
        self.assertEqual(credentials.materialise(home=self.home, environ={"CODEX_AUTH": ""}), [])

    def test_non_json_value_is_written_verbatim(self):
        credentials.materialise(home=self.home, environ={"CLAUDE_CREDENTIALS": "opaque-token"})
        self.assertEqual((self.home / ".claude" / ".credentials.json").read_text(), "opaque-token")

    def test_whitespace_is_stripped_from_json(self):
        credentials.materialise(home=self.home, environ={"CODEX_AUTH": f"\n  {AUTH}\n "})
        self.assertEqual(
            json.loads((self.home / ".codex" / "auth.json").read_text())["auth_mode"], "chatgpt"
        )

    def test_multiple_credentials(self):
        written = credentials.materialise(
            home=self.home, environ={"CODEX_AUTH": AUTH, "GEMINI_OAUTH_CREDS": "{}"}
        )
        self.assertCountEqual(written, ["CODEX_AUTH", "GEMINI_OAUTH_CREDS"])


class AgentEnvTests(unittest.TestCase):
    def test_materialised_credentials_are_dropped_from_the_agent_env(self):
        """The CLI reads the file; the raw token need not also sit in the env."""
        with tempfile.TemporaryDirectory() as tmp:
            env = {"HOME": tmp, "CODEX_AUTH": AUTH, "OPENAI_API_KEY": "sk-x", "GITHUB_TOKEN": "ghp_x"}
            with mock.patch.dict(os.environ, env, clear=True):
                result = worker.agent_env()
            self.assertNotIn("CODEX_AUTH", result)
            self.assertNotIn("GITHUB_TOKEN", result)
            self.assertEqual(result["OPENAI_API_KEY"], "sk-x")
            self.assertTrue((Path(tmp) / ".codex" / "auth.json").exists())


if __name__ == "__main__":
    unittest.main()
