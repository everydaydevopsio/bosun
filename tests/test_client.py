import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from bosun.client import BridgeClient, BridgeError, discover_target, state_dir
from fakebridge import FakeBridge, FakeBridgeServer


class StateDirTests(unittest.TestCase):
    def test_respects_override(self):
        with mock.patch.dict(os.environ, {"BRIDGECTL_STATE_DIR": "/custom/state"}):
            self.assertEqual(state_dir(), Path("/custom/state"))

    def test_defaults_to_config_bridgectl(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertTrue(str(state_dir()).endswith(".config/bridgectl"))


class DiscoverTargetTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)

    def test_none_when_nothing_is_running(self):
        self.assertIsNone(discover_target(self.dir))

    def test_prefers_the_unix_socket(self):
        (self.dir / "server.sock").touch()
        self.assertEqual(discover_target(self.dir), f"unix://{self.dir / 'server.sock'}")

    def test_falls_back_to_addr_file(self):
        (self.dir / "server.addr").write_text("127.0.0.1:9445\n")
        self.assertEqual(discover_target(self.dir), "127.0.0.1:9445")

    def test_addr_file_holding_a_path_becomes_a_unix_target(self):
        (self.dir / "server.addr").write_text("/run/bridge/server.sock\n")
        self.assertEqual(discover_target(self.dir), "unix:///run/bridge/server.sock")


class ClientTests(unittest.TestCase):
    def test_health_and_providers(self):
        with FakeBridgeServer() as server:
            with BridgeClient(server.target) as client:
                self.assertEqual(client.health().status, "ok")
                self.assertEqual(client.list_providers()[0].provider, "echo")

    def test_connect_discovers_a_running_server(self):
        with FakeBridgeServer() as server:
            client = BridgeClient.connect(directory=server.state_dir, autostart=False)
            self.addCleanup(client.close)
            self.assertEqual(client.target, server.target)

    def test_connect_without_a_server_and_no_autostart(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(BridgeError):
                BridgeClient.connect(directory=Path(tmp), autostart=False)

    def test_autostart_timeout_is_reported(self):
        """A server that starts but never binds must fail with a clear error."""
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(BridgeError) as ctx:
                BridgeClient.connect(directory=Path(tmp), timeout=1, binary="true")
            self.assertIn("did not start", str(ctx.exception))

    def test_missing_binary_is_reported(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(BridgeError) as ctx:
                BridgeClient.connect(directory=Path(tmp), timeout=1, binary="definitely-not-bridgectl")
            self.assertIn("could not run", str(ctx.exception))

    def test_autostart_passes_a_scrubbed_environment(self):
        """The server forks the provider, so its env is the agent's env."""
        with tempfile.TemporaryDirectory() as tmp:
            with mock.patch("bosun.client.subprocess.Popen") as popen:
                with self.assertRaises(BridgeError):
                    BridgeClient.connect(
                        directory=Path(tmp), timeout=1, binary="bridgectl",
                        env={"OPENAI_API_KEY": "sk-x"},
                    )
            self.assertEqual(popen.call_args.kwargs["env"], {"OPENAI_API_KEY": "sk-x"})

    def test_start_session_sends_the_workspace_and_provider(self):
        service = FakeBridge()
        with FakeBridgeServer(service) as server:
            with BridgeClient(server.target) as client:
                client.start_session("s1", "bosun-review", "/repos/ws", "codex")
        started = service.started[0]
        self.assertEqual(started.repo_path, "/repos/ws")
        self.assertEqual(started.provider, "codex")
        self.assertEqual(started.project_id, "bosun-review")

    def test_start_session_error_is_wrapped(self):
        service = FakeBridge(fail_start="provider codex is unavailable")
        with FakeBridgeServer(service) as server:
            with BridgeClient(server.target) as client:
                with self.assertRaises(BridgeError) as ctx:
                    client.start_session("s1", "p", "/ws", "codex")
        self.assertIn("provider codex is unavailable", str(ctx.exception))

    def test_write_input_reaches_the_session(self):
        service = FakeBridge()
        with FakeBridgeServer(service) as server:
            with BridgeClient(server.target) as client:
                client.start_session("s1", "p", "/ws", "echo")
                client.write_input("s1", "c1", b"hello\n")
        self.assertEqual(service.inputs, [b"hello\n"])

    def test_stop_session_is_best_effort(self):
        """A stop against a dead server must not raise."""
        with FakeBridgeServer() as server:
            client = BridgeClient(server.target, timeout=1)
        self.assertIsNone(client.stop_session("s1"))
        client.close()


if __name__ == "__main__":
    unittest.main()
