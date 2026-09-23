import unittest
from types import SimpleNamespace
from unittest import mock

from bosun import jobs


def job(succeeded=0, failed=0, status=True):
    return SimpleNamespace(
        status=SimpleNamespace(succeeded=succeeded, failed=failed) if status else None
    )


class ActiveReviewTests(unittest.TestCase):
    def count(self, items):
        batch = mock.Mock()
        batch.list_namespaced_job.return_value = SimpleNamespace(items=items)
        return jobs.active_reviews(batch)

    def test_no_jobs(self):
        self.assertEqual(self.count([]), 0)

    def test_running_jobs_are_counted(self):
        self.assertEqual(self.count([job(), job()]), 2)

    def test_finished_jobs_are_not_counted(self):
        self.assertEqual(self.count([job(succeeded=1), job(failed=1)]), 0)

    def test_mixed(self):
        self.assertEqual(self.count([job(succeeded=1), job(), job(failed=1), job()]), 2)

    def test_job_without_status_counts_as_running(self):
        self.assertEqual(self.count([job(status=False)]), 1)


class CapTests(unittest.TestCase):
    def submit(self, running, cap=3):
        with mock.patch.object(jobs.config, "load_incluster_config"), \
             mock.patch.object(jobs.client, "BatchV1Api") as api, \
             mock.patch.object(jobs.settings, "MAX_CONCURRENT_REVIEWS", cap), \
             mock.patch.object(jobs, "active_reviews", return_value=running):
            jobs.submit_review("acme/widget", "main", "", "d1", None, "branch-created")
            return api.return_value.create_namespaced_job

    def test_default_cap_is_three(self):
        import importlib
        from bosun import config
        self.assertEqual(importlib.reload(config).MAX_CONCURRENT_REVIEWS, 3)

    def test_below_the_cap_submits(self):
        self.submit(running=2).assert_called_once()

    def test_at_the_cap_rejects(self):
        with self.assertRaises(jobs.AtCapacity):
            self.submit(running=3)

    def test_above_the_cap_rejects(self):
        with self.assertRaises(jobs.AtCapacity):
            self.submit(running=9)

    def test_cap_is_configurable(self):
        self.submit(running=4, cap=5).assert_called_once()


class DeadlineTests(unittest.TestCase):
    def test_job_deadline_derives_from_the_single_timeout(self):
        from bosun import config
        self.assertEqual(config.REVIEW_TIMEOUT_SECONDS, 1800)
        self.assertEqual(
            config.JOB_DEADLINE_SECONDS,
            config.REVIEW_TIMEOUT_SECONDS + config.JOB_DEADLINE_GRACE_SECONDS,
        )
        self.assertGreater(config.JOB_DEADLINE_SECONDS, config.REVIEW_TIMEOUT_SECONDS)


if __name__ == "__main__":
    unittest.main()
