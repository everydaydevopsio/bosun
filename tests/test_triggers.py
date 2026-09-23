import unittest
from bosun.app import review_request


def comment_payload(body="please @bridgectl review this", association="MEMBER", **extra):
    payload = {
        "repository": {"full_name": "acme/widget"},
        "action": "created",
        "comment": {"body": body, "author_association": association},
        "issue": {"number": 42, "pull_request": {"url": "https://api.github.com/pulls/42"}},
    }
    payload.update(extra)
    return payload


class TriggerTests(unittest.TestCase):
    def test_branch_create(self):
        payload = {"repository": {"full_name": "acme/widget"}, "ref_type": "branch", "ref": "feature/x", "master_branch": "main"}
        self.assertEqual(review_request("create", payload)[:2], ("acme/widget", "feature/x"))

    def test_branch_create_leaves_sha_empty(self):
        """The create event has no head SHA; master_branch must not be used as one."""
        payload = {"repository": {"full_name": "acme/widget"}, "ref_type": "branch", "ref": "feature/x", "master_branch": "main"}
        self.assertEqual(review_request("create", payload)[2], "")

    def test_tag_create_ignored(self):
        payload = {"repository": {"full_name": "acme/widget"}, "ref_type": "tag", "ref": "v1"}
        self.assertIsNone(review_request("create", payload))

    def test_pull_request_opened(self):
        payload = {
            "action": "opened",
            "repository": {"full_name": "acme/widget"},
            "pull_request": {"number": 7, "head": {"ref": "feature/x", "sha": "abc123"}},
        }
        self.assertEqual(review_request("pull_request", payload), ("acme/widget", "feature/x", "abc123", 7, "pull-request-opened"))

    def test_pull_request_closed_ignored(self):
        payload = {
            "action": "closed",
            "repository": {"full_name": "acme/widget"},
            "pull_request": {"number": 7, "head": {"ref": "feature/x", "sha": "abc123"}},
        }
        self.assertIsNone(review_request("pull_request", payload))

    def test_review_comment(self):
        req = review_request("issue_comment", comment_payload())
        self.assertEqual(req[3], 42)
        self.assertEqual(req[4], "comment")

    def test_unrelated_comment_ignored(self):
        self.assertIsNone(review_request("issue_comment", comment_payload(body="looks good")))

    def test_unauthorized_author_ignored(self):
        """An outside account must not be able to spend model credits."""
        self.assertIsNone(review_request("issue_comment", comment_payload(association="NONE")))

    def test_bot_comment_ignored(self):
        """Bosun's own review must not re-trigger itself."""
        payload = comment_payload(
            body="## Bosun code review\n\nrerun with @bridgectl review",
            sender={"type": "Bot", "login": "bosun[bot]"},
        )
        self.assertIsNone(review_request("issue_comment", payload))

    def test_plain_issue_comment_ignored(self):
        """issue_comment also fires for non-PR issues, which cannot be reviewed."""
        payload = comment_payload()
        payload["issue"] = {"number": 42}
        self.assertIsNone(review_request("issue_comment", payload))

    def test_review_comment_on_pull_request_review(self):
        payload = {
            "repository": {"full_name": "acme/widget"},
            "action": "created",
            "comment": {"body": "@bridgectl review", "author_association": "OWNER"},
            "pull_request": {"number": 11},
        }
        self.assertEqual(review_request("pull_request_review_comment", payload)[3], 11)

    def test_missing_repository_ignored(self):
        self.assertIsNone(review_request("pull_request", {"action": "opened"}))


if __name__ == "__main__":
    unittest.main()
