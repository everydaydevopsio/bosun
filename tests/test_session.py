import unittest
from unittest import mock

from bosun.client import BridgeClient
from bosun.session import (
    ReviewFailed,
    clean_output,
    looks_like_terminal_ui,
    run_review,
    strip_prompt_echo,
)
from fakebridge import FakeBridge, FakeBridgeServer


class CleanOutputTests(unittest.TestCase):
    def test_strips_csi_sequences(self):
        self.assertEqual(clean_output("\x1b[1;32mhello\x1b[0m"), "hello")

    def test_strips_osc_sequences(self):
        self.assertEqual(clean_output("\x1b]0;title\x07body"), "body")

    def test_normalises_carriage_returns(self):
        self.assertEqual(clean_output("a\r\nb\rc"), "a\nb\nc")

    def test_drops_control_characters(self):
        self.assertEqual(clean_output("a\x00\x08b"), "ab")

    def test_preserves_markdown(self):
        text = "## Summary\n\n- item one\n- item two"
        self.assertEqual(clean_output(text), text)


class StripPromptEchoTests(unittest.TestCase):
    def test_removes_echoed_prompt(self):
        prompt = "review this\ncarefully"
        output = "review this\ncarefully\n## Findings\nnone"
        self.assertEqual(strip_prompt_echo(output, prompt), "## Findings\nnone")

    def test_keeps_output_when_prompt_not_echoed(self):
        output = "## Findings\nnone"
        self.assertEqual(strip_prompt_echo(output, "review this\ncarefully"), output)

    def test_empty_prompt(self):
        self.assertEqual(strip_prompt_echo("abc", ""), "abc")


class RunReviewTests(unittest.TestCase):
    def review(self, service, prompt="do the review", **kwargs):
        kwargs.setdefault("idle_seconds", 1)
        kwargs.setdefault("max_seconds", 20)
        with FakeBridgeServer(service) as server:
            with BridgeClient(server.target) as client:
                return run_review("/ws", "echo", prompt, client=client, **kwargs)

    def test_returns_the_agent_output(self):
        self.assertEqual(self.review(FakeBridge()), "## Review\nlooks fine")

    def test_prompt_is_sent_with_write_input(self):
        service = FakeBridge()
        self.review(service, prompt="please review")
        self.assertEqual(service.inputs, [b"please review\n"])

    def test_echoed_prompt_is_stripped_from_the_result(self):
        service = FakeBridge(echo_input=True)
        self.assertEqual(self.review(service, prompt="please review"), "## Review\nlooks fine")

    def test_session_is_stopped_explicitly(self):
        """Bosun ends the session by RPC, never by closing stdin."""
        service = FakeBridge()
        self.review(service)
        self.assertEqual(len(service.stopped), 1)
        self.assertFalse(service.stopped[0].force)

    def test_session_exit_ends_the_wait_immediately(self):
        service = FakeBridge(exit_after_reply=True)
        self.assertEqual(self.review(service, idle_seconds=60), "## Review\nlooks fine")

    def test_slow_provider_is_not_cut_off(self):
        service = FakeBridge(reply_delay=2.0, echo_input=False)
        self.assertEqual(self.review(service, idle_seconds=3, max_seconds=30), "## Review\nlooks fine")

    def test_no_output_raises(self):
        service = FakeBridge(reply=b"", echo_input=False)
        with self.assertRaises(ReviewFailed) as ctx:
            self.review(service)
        self.assertIn("no output", str(ctx.exception))

    def test_attach_error_raises(self):
        service = FakeBridge(attach_error="provider failed to launch")
        with self.assertRaises(ReviewFailed) as ctx:
            self.review(service, attach_timeout=3)
        self.assertIn("provider failed to launch", str(ctx.exception))

    def test_start_failure_raises(self):
        service = FakeBridge(fail_start="unknown provider")
        with self.assertRaises(Exception) as ctx:
            self.review(service)
        self.assertIn("unknown provider", str(ctx.exception))

    def test_ansi_is_stripped_from_provider_output(self):
        service = FakeBridge(reply=b"\x1b[1m## Bold finding\x1b[0m\r\n", echo_input=False)
        self.assertEqual(self.review(service), "## Bold finding")

    def test_max_seconds_bounds_a_silent_provider(self):
        """A provider that never answers must not hang the Job."""
        service = FakeBridge(reply=b"", echo_input=False)
        with self.assertRaises(ReviewFailed):
            self.review(service, idle_seconds=30, max_seconds=2)


class ProviderPreflightTests(unittest.TestCase):
    """An unavailable provider should fail with a message that names the fix."""

    def review(self, provider):
        with FakeBridgeServer(FakeBridge()) as server:
            with BridgeClient(server.target) as client:
                return run_review("/ws", provider, "p", client=client, idle_seconds=1, max_seconds=10)

    def test_unknown_provider_lists_the_known_ones(self):
        with self.assertRaises(ReviewFailed) as ctx:
            self.review("nope")
        self.assertIn("unknown provider", str(ctx.exception))
        self.assertIn("echo", str(ctx.exception))

    def test_available_provider_runs(self):
        self.assertEqual(self.review("echo"), "## Review\nlooks fine")


class ThinkingTests(unittest.TestCase):
    """Stream-JSON providers emit reasoning separately from their answer."""

    def review(self, service, **kwargs):
        kwargs.setdefault("idle_seconds", 1)
        kwargs.setdefault("max_seconds", 20)
        with FakeBridgeServer(service) as server:
            with BridgeClient(server.target) as client:
                return run_review("/ws", "echo", "p", client=client, **kwargs)

    def test_thinking_is_kept_out_of_the_review_body(self):
        service = FakeBridge(thinking=["considering the diff", "checking tests"], echo_input=False)
        output = self.review(service)
        self.assertEqual(output, "## Review\nlooks fine")
        self.assertNotIn("considering the diff", output)

    def test_thinking_can_be_included_on_request(self):
        service = FakeBridge(thinking=["weighing options"], echo_input=False)
        with mock.patch("bosun.session.INCLUDE_THINKING", True):
            output = self.review(service)
        self.assertIn("weighing options", output)
        self.assertIn("<details>", output)

    def test_thinking_keeps_the_session_alive(self):
        """A long reasoning phase must not read as an idle session."""
        service = FakeBridge(thinking=["a", "b", "c", "d"], reply_delay=1.5, echo_input=False)
        self.assertEqual(self.review(service, idle_seconds=1), "## Review\nlooks fine")

    def test_reasoning_without_an_answer_still_fails(self):
        service = FakeBridge(reply=b"", thinking=["thought hard"], echo_input=False)
        with self.assertRaises(ReviewFailed):
            self.review(service)


class TerminalUIGuardTests(unittest.TestCase):
    """A full-screen TUI provider must never have its repaints posted to a PR."""

    def test_detects_box_drawing_repaints(self):
        frame = "╭───────────────────────────╮│ >_ OpenAI Codex           │╰───────────────────────────╯"
        self.assertTrue(looks_like_terminal_ui(frame * 20))

    def test_prose_is_not_flagged(self):
        review = "## Summary\n\nNo material findings. The diff is small and tests pass.\n"
        self.assertFalse(looks_like_terminal_ui(review))

    def test_markdown_tables_are_not_flagged(self):
        table = "| col | col |\n| --- | --- |\n| a | b |\n" * 20
        self.assertFalse(looks_like_terminal_ui(table))

    def test_empty_is_not_flagged(self):
        self.assertFalse(looks_like_terminal_ui(""))

    def test_review_fails_rather_than_posting_noise(self):
        frame = "╭─────────────────╮│ codex           │╰─────────────────╯"
        service = FakeBridge(reply=(frame * 30).encode(), echo_input=False)
        with FakeBridgeServer(service) as server:
            with BridgeClient(server.target) as client:
                with self.assertRaises(ReviewFailed) as ctx:
                    run_review("/ws", "echo", "p", client=client, idle_seconds=1, max_seconds=15)
        self.assertIn("terminal UI output", str(ctx.exception))


class AllowTUIOverrideTests(unittest.TestCase):
    def test_override_keeps_the_raw_output(self):
        frame = "╭─────────────────╮│ codex           │╰─────────────────╯"
        service = FakeBridge(reply=(frame * 30).encode(), echo_input=False)
        with mock.patch("bosun.session.ALLOW_TUI_OUTPUT", True):
            with FakeBridgeServer(service) as server:
                with BridgeClient(server.target) as client:
                    output = run_review("/ws", "echo", "p", client=client,
                                        idle_seconds=1, max_seconds=15)
        self.assertIn("codex", output)

    def test_guard_message_names_the_override(self):
        frame = "╭─────────────────╮│ codex           │╰─────────────────╯"
        service = FakeBridge(reply=(frame * 30).encode(), echo_input=False)
        with FakeBridgeServer(service) as server:
            with BridgeClient(server.target) as client:
                with self.assertRaises(ReviewFailed) as ctx:
                    run_review("/ws", "echo", "p", client=client, idle_seconds=1, max_seconds=15)
        self.assertIn("BOSUN_ALLOW_TUI_OUTPUT", str(ctx.exception))


class PromptDeliveryTests(unittest.TestCase):
    """Non-interactive providers take the prompt on the command line."""

    def review(self, provider, **kwargs):
        kwargs.setdefault("idle_seconds", 1)
        kwargs.setdefault("max_seconds", 15)
        service = FakeBridge(echo_input=False, exit_after_reply=True)
        with FakeBridgeServer(service) as server:
            with BridgeClient(server.target) as client:
                out = run_review("/ws", provider, "review this", client=client, **kwargs)
        return service, out

    def test_interactive_provider_uses_write_input(self):
        service, _ = self.review("echo")
        self.assertEqual(service.inputs, [b"review this\n"])
        self.assertEqual(dict(service.started[0].agent_opts), {})

    def test_exec_provider_passes_the_prompt_as_an_argument(self):
        with mock.patch("bosun.session.PROMPT_ARG_PROVIDERS", frozenset({"echo"})):
            service, _ = self.review("echo")
        self.assertEqual(service.inputs, [])
        self.assertEqual(dict(service.started[0].agent_opts), {"arg:prompt": "review this"})

    def test_only_one_arg_option_is_sent(self):
        """bridgectl ranges over a Go map, so multiple args would be unordered."""
        with mock.patch("bosun.session.PROMPT_ARG_PROVIDERS", frozenset({"echo"})):
            service, _ = self.review("echo")
        args = [k for k in service.started[0].agent_opts if k.startswith("arg:")]
        self.assertEqual(len(args), 1)


if __name__ == "__main__":
    unittest.main()
