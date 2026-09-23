import unittest

from bosun.jobs import MAX_NAME, _name


class JobNameTests(unittest.TestCase):
    def test_within_dns_limit(self):
        name = _name("acme/widget", "a" * 200, "delivery-id")
        self.assertLessEqual(len(name), MAX_NAME)

    def test_lowercase_dns_safe(self):
        name = _name("Acme/Widget", "Feature/Some_Branch", "D1")
        self.assertRegex(name, r"^[a-z0-9]([a-z0-9-]*[a-z0-9])?$")

    def test_long_branches_do_not_collide(self):
        """The uniqueness suffix must survive truncation of a long branch name."""
        branch = "feature/a-really-long-branch-name-that-goes-on-and-on-forever-yes"
        first = _name("acme/widget", branch, "delivery-one")
        second = _name("acme/widget", branch, "delivery-two")
        self.assertNotEqual(first, second)
        self.assertLessEqual(len(first), MAX_NAME)

    def test_same_delivery_is_stable(self):
        """Retried deliveries must map to the same Job so they dedupe via 409."""
        args = ("acme/widget", "feature/x", "delivery-one")
        self.assertEqual(_name(*args), _name(*args))

    def test_different_repos_do_not_collide(self):
        self.assertNotEqual(
            _name("acme/widget", "feature/x", "d"),
            _name("other/widget", "feature/x", "d"),
        )

    def test_empty_ref(self):
        self.assertRegex(_name("acme/widget", "", "d"), r"^[a-z0-9]([a-z0-9-]*[a-z0-9])?$")


if __name__ == "__main__":
    unittest.main()
