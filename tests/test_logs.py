import io
import json
import logging
import unittest
from unittest import mock

from bosun import logs


class FormatterTests(unittest.TestCase):
    def record(self, **extra):
        record = logging.LogRecord("bosun.test", logging.INFO, __file__, 1, "hello", None, None)
        for key, value in extra.items():
            setattr(record, key, value)
        return record

    def test_text_format_matches_slog_shape(self):
        line = logs.TextFormatter().format(self.record(context={"repo": "acme/widget", "n": 3}))
        self.assertIn('msg="hello"', line)
        self.assertIn("level=INFO", line)
        self.assertIn('repo="acme/widget"', line)
        self.assertIn("n=3", line)
        self.assertTrue(line.startswith("time="))

    def test_json_format_is_parseable(self):
        payload = json.loads(logs.JSONFormatter().format(self.record(context={"job": "review-x"})))
        self.assertEqual(payload["msg"], "hello")
        self.assertEqual(payload["level"], "INFO")
        self.assertEqual(payload["job"], "review-x")
        self.assertEqual(payload["logger"], "bosun.test")

    def test_json_format_survives_odd_values(self):
        payload = json.loads(logs.JSONFormatter().format(self.record(context={"path": object()})))
        self.assertIsInstance(payload["path"], str)


class ConfigureTests(unittest.TestCase):
    def tearDown(self):
        logs._CONFIGURED = False
        logs.configure(force=True)

    def test_level_comes_from_the_environment(self):
        with mock.patch.dict("os.environ", {"BOSUN_LOG_LEVEL": "debug"}):
            logs.configure(force=True)
        self.assertEqual(logging.getLogger().level, logging.DEBUG)

    def test_json_format_selected_by_environment(self):
        with mock.patch.dict("os.environ", {"BOSUN_LOG_FORMAT": "json"}):
            logs.configure(force=True)
        self.assertIsInstance(logging.getLogger().handlers[0].formatter, logs.JSONFormatter)

    def test_library_logs_are_quietened(self):
        with mock.patch.dict("os.environ", {"BOSUN_LOG_LEVEL": "info"}):
            logs.configure(force=True)
        self.assertGreaterEqual(logging.getLogger("httpx").level, logging.WARNING)

    def test_emits_to_stderr(self):
        logs.configure(force=True)
        stream = io.StringIO()
        logging.getLogger().handlers[0].stream = stream
        logs.get_logger("bosun.test").info("wrote", extra={"context": {"k": "v"}})
        self.assertIn('msg="wrote"', stream.getvalue())
        self.assertIn('k="v"', stream.getvalue())


if __name__ == "__main__":
    unittest.main()
