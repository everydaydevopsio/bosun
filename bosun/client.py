"""A minimal bridgectl client.

bridgectl's CLI is a thin wrapper over a gRPC ``BridgeService``. In local mode
the server listens on a unix socket at ``$BRIDGECTL_STATE_DIR/server.sock``
(default ``~/.config/bridgectl``) with no TLS, which is exactly the shape Bosun
needs: the reviewer Job owns its own container, so the server is always local.

Talking to that API directly — rather than piping a prompt into
``bridgectl session start --no-tty`` — means Bosun controls the session
lifecycle explicitly. It sends the prompt with ``WriteInput`` and ends the
session with ``StopSession``, so it never has to close stdin, which the CLI
treats as "the operator left" and answers with a forced stop.

The equivalent Go client is ``pkg/bridgeclient``; see ``examples/orchestrator``
upstream.
"""

import os
import pathlib
import subprocess
import sys
import time
import uuid

import grpc

_GEN = pathlib.Path(__file__).parent / "gen"
if str(_GEN) not in sys.path:
    sys.path.insert(0, str(_GEN))

from bridge.v1 import bridge_pb2 as pb  # noqa: E402
from bridge.v1 import bridge_pb2_grpc as pb_grpc  # noqa: E402

from .logs import get_logger  # noqa: E402

log = get_logger("bosun.client")

DEFAULT_START_TIMEOUT = 30


class BridgeError(RuntimeError):
    pass


def state_dir() -> pathlib.Path:
    """Mirrors bridgectl's localserver.StateDir()."""
    override = os.getenv("BRIDGECTL_STATE_DIR")
    if override:
        return pathlib.Path(override)
    return pathlib.Path(os.path.expanduser("~")) / ".config" / "bridgectl"


def discover_target(directory: pathlib.Path | None = None) -> str | None:
    """Return a gRPC target for a running local server, or None.

    Only local mode is supported: the reviewer runs the server in its own
    container, so mTLS/JWT discovery would be dead code here.
    """
    directory = directory or state_dir()
    socket = directory / "server.sock"
    if socket.exists():
        return f"unix://{socket}"
    addr_file = directory / "server.addr"
    if addr_file.exists():
        addr = addr_file.read_text().strip()
        if addr:
            return f"unix://{addr}" if addr.startswith("/") else addr
    return None


class BridgeClient:
    """Connects to (and if needed starts) a local bridge server."""

    def __init__(self, target: str, timeout: int = DEFAULT_START_TIMEOUT):
        self.target = target
        self.timeout = timeout
        self._channel = grpc.insecure_channel(target)
        self.stub = pb_grpc.BridgeServiceStub(self._channel)

    # -- lifecycle ---------------------------------------------------------
    @classmethod
    def connect(
        cls,
        directory: pathlib.Path | None = None,
        timeout: int = DEFAULT_START_TIMEOUT,
        autostart: bool = True,
        binary: str = "bridgectl",
        env: dict | None = None,
    ) -> "BridgeClient":
        directory = directory or state_dir()
        target = discover_target(directory)
        if target is None:
            if not autostart:
                raise BridgeError(f"no bridge server found in {directory}")
            target = cls._start_server(directory, timeout, binary, env)
        client = cls(target, timeout)
        client.health()
        return client

    @staticmethod
    def _start_server(directory: pathlib.Path, timeout: int, binary: str, env: dict | None = None) -> str:
        """Spawn `bridgectl server start`, as the CLI's ensureServer does.

        The server is what forks the AI provider, so the provider inherits this
        environment. Callers pass a scrubbed env to keep Bosun's GitHub token
        away from an agent that is reading untrusted repository content.
        """
        directory.mkdir(parents=True, exist_ok=True)
        log.info("starting bridge server (%s server start)", binary)
        try:
            subprocess.Popen(
                [binary, "server", "start"],
                stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                start_new_session=True, env=env,
            )
        except OSError as exc:
            raise BridgeError(f"could not run {binary!r}: {exc}") from exc
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            target = discover_target(directory)
            if target:
                return target
            time.sleep(0.2)
        raise BridgeError(f"bridge server did not start within {timeout}s")

    def close(self) -> None:
        self._channel.close()

    def __enter__(self) -> "BridgeClient":
        return self

    def __exit__(self, *exc) -> None:
        self.close()

    # -- RPCs --------------------------------------------------------------
    def health(self):
        try:
            return self.stub.Health(pb.HealthRequest(), timeout=self.timeout)
        except grpc.RpcError as exc:
            raise BridgeError(f"bridge server is not healthy: {_rpc_detail(exc)}") from exc

    def list_providers(self):
        return self.stub.ListProviders(pb.ListProvidersRequest(), timeout=self.timeout).providers

    def start_session(self, session_id: str, project: str, repo_path: str, provider: str,
                      cols: int = 200, rows: int = 50):
        try:
            return self.stub.StartSession(
                pb.StartSessionRequest(
                    project_id=project,
                    session_id=session_id,
                    repo_path=str(repo_path),
                    provider=provider,
                    # A wide terminal keeps the provider from hard-wrapping the
                    # Markdown it writes, which would survive into the PR comment.
                    initial_cols=cols,
                    initial_rows=rows,
                ),
                timeout=self.timeout,
            )
        except grpc.RpcError as exc:
            raise BridgeError(f"start session: {_rpc_detail(exc)}") from exc

    def attach(self, session_id: str, client_id: str, after_seq: int = 0):
        """Open the event stream as the active writer."""
        return self.stub.AttachSession(
            pb.AttachSessionRequest(
                session_id=session_id,
                client_id=client_id,
                after_seq=after_seq,
                role=pb.ATTACH_ROLE_WRITER,
            )
        )

    def write_input(self, session_id: str, client_id: str, data: bytes):
        try:
            return self.stub.WriteInput(
                pb.WriteInputRequest(session_id=session_id, client_id=client_id, data=data),
                timeout=self.timeout,
            )
        except grpc.RpcError as exc:
            raise BridgeError(f"write input: {_rpc_detail(exc)}") from exc

    def stop_session(self, session_id: str, force: bool = False):
        try:
            return self.stub.StopSession(
                pb.StopSessionRequest(session_id=session_id, force=force), timeout=self.timeout
            )
        except grpc.RpcError:
            # The session may already be gone; stopping is best-effort.
            return None

    def get_session(self, session_id: str):
        try:
            return self.stub.GetSession(pb.GetSessionRequest(session_id=session_id), timeout=self.timeout)
        except grpc.RpcError:
            return None


def new_session_id() -> str:
    return str(uuid.uuid4())


def _rpc_detail(exc: grpc.RpcError) -> str:
    details = getattr(exc, "details", lambda: None)()
    code = getattr(exc, "code", lambda: None)()
    return f"{code.name if code else 'ERROR'}: {details or exc}"
