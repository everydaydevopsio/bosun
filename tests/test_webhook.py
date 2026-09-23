import hashlib
import hmac
import json
import unittest
from unittest import mock

from fastapi.testclient import TestClient

import bosun.app as app_mod
from bosun.github import verify_signature

SECRET = "s3cr3t"


def sign(body: bytes, secret: str = SECRET) -> str:
    return "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()


class SignatureTests(unittest.TestCase):
    def test_valid_signature(self):
        self.assertTrue(verify_signature(SECRET, b"{}", sign(b"{}")))

    def test_wrong_secret(self):
        self.assertFalse(verify_signature("other", b"{}", sign(b"{}")))

    def test_missing_signature(self):
        self.assertFalse(verify_signature(SECRET, b"{}", None))

    def test_wrong_prefix(self):
        self.assertFalse(verify_signature(SECRET, b"{}", "sha1=abcdef"))

    def test_empty_secret_fails_closed(self):
        """An unconfigured secret must reject deliveries, not accept forged ones."""
        self.assertFalse(verify_signature("", b"{}", sign(b"{}", "")))


class WebhookTests(unittest.TestCase):
    def setUp(self):
        patcher = mock.patch.object(app_mod, "WEBHOOK_SECRET", SECRET)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.submit = mock.patch.object(app_mod, "submit_review", return_value="job-1").start()
        self.addCleanup(mock.patch.stopall)
        self.client = TestClient(app_mod.app)

    def post(self, event, payload, signature=None, delivery="d1"):
        body = json.dumps(payload).encode()
        return self.client.post(
            "/webhooks/github",
            content=body,
            headers={
                "X-GitHub-Event": event,
                "X-GitHub-Delivery": delivery,
                "X-Hub-Signature-256": signature or sign(body),
            },
        )

    def test_healthz(self):
        self.assertEqual(self.client.get("/healthz").json(), {"ok": True})

    def test_rejects_bad_signature(self):
        r = self.post("create", {}, signature="sha256=" + "0" * 64)
        self.assertEqual(r.status_code, 401)

    def test_rejects_malformed_json(self):
        body = b"not json"
        r = self.client.post(
            "/webhooks/github",
            content=body,
            headers={"X-GitHub-Event": "push", "X-Hub-Signature-256": sign(body)},
        )
        self.assertEqual(r.status_code, 400)

    def test_rejects_non_object_payload(self):
        r = self.post("push", [1, 2, 3])
        self.assertEqual(r.status_code, 400)

    def test_branch_create_submits_empty_sha(self):
        r = self.post("create", {
            "repository": {"full_name": "acme/widget"},
            "ref_type": "branch", "ref": "feature/x", "master_branch": "main",
        })
        self.assertEqual(r.status_code, 200)
        self.assertTrue(r.json()["accepted"])
        self.assertEqual(self.submit.call_args.args[2], "")

    def test_ignored_event(self):
        r = self.post("push", {"repository": {"full_name": "acme/widget"}})
        self.assertEqual(r.json()["accepted"], False)
        self.submit.assert_not_called()

    def test_duplicate_delivery_is_not_an_error(self):
        self.submit.side_effect = app_mod.JobExists("review-x")
        r = self.post("create", {
            "repository": {"full_name": "acme/widget"},
            "ref_type": "branch", "ref": "feature/x",
        })
        self.assertEqual(r.status_code, 200)
        self.assertTrue(r.json()["duplicate"])


if __name__ == "__main__":
    unittest.main()
