import unittest
from bosun.app import review_request

class TriggerTests(unittest.TestCase):
    def test_branch_create(self):
        payload = {"repository": {"full_name": "acme/widget"}, "ref_type": "branch", "ref": "feature/x", "master_branch": "main"}
        self.assertEqual(review_request("create", payload)[:2], ("acme/widget", "feature/x"))

    def test_review_comment(self):
        payload = {
            "repository": {"full_name": "acme/widget"},
            "action": "created",
            "comment": {"body": "please @bridgectl review this"},
            "issue": {"number": 42},
        }
        req = review_request("issue_comment", payload)
        self.assertEqual(req[3], 42)
        self.assertEqual(req[4], "comment")

    def test_unrelated_comment_ignored(self):
        payload = {
            "repository": {"full_name": "acme/widget"},
            "action": "created",
            "comment": {"body": "looks good"},
            "issue": {"number": 42},
        }
        self.assertIsNone(review_request("issue_comment", payload))

if __name__ == "__main__":
    unittest.main()
