"""An in-process BridgeService, so the client is tested against real gRPC.

The fake implements just enough of bridgectl's contract to exercise Bosun's
client: sessions, a writer attach stream, WriteInput, and StopSession.
"""

import queue
import tempfile
import threading
import time
from concurrent import futures
from pathlib import Path

import grpc

from bosun.client import pb, pb_grpc


class FakeBridge(pb_grpc.BridgeServiceServicer):
    def __init__(self, reply=b"## Review\r\nlooks fine\r\n", echo_input=True,
                 reply_delay=0.0, fail_start=None, attach_error=None, exit_after_reply=False,
                 thinking=()):
        self.reply = reply
        self.echo_input = echo_input
        self.reply_delay = reply_delay
        self.fail_start = fail_start
        self.attach_error = attach_error
        self.exit_after_reply = exit_after_reply
        self.thinking = list(thinking)
        self.started = []
        self.stopped = []
        self.inputs = []
        self._events: queue.Queue = queue.Queue()
        self._seq = 0

    # -- helpers -----------------------------------------------------------
    def _emit(self, **kwargs):
        self._seq += 1
        self._events.put(pb.AttachSessionEvent(seq=self._seq, **kwargs))

    # -- RPCs --------------------------------------------------------------
    def Health(self, request, context):
        return pb.HealthResponse(status="ok", server_instance_id="fake")

    def ListProviders(self, request, context):
        return pb.ListProvidersResponse(
            providers=[pb.ProviderInfo(provider="echo", available=True, binary="echo", version="1")]
        )

    def StartSession(self, request, context):
        if self.fail_start:
            context.abort(grpc.StatusCode.FAILED_PRECONDITION, self.fail_start)
        self.started.append(request)
        # A non-interactive provider gets its prompt on the command line and
        # starts working immediately, with no WriteInput to wait for.
        prompt_args = [v for k, v in request.agent_opts.items() if k.startswith("arg:")]
        if prompt_args:
            self._respond_async(prompt_args[0].encode())
        return pb.StartSessionResponse(
            session_id=request.session_id, status=pb.SESSION_STATUS_RUNNING
        )

    def _respond_async(self, echo: bytes):
        def respond():
            if self.echo_input:
                self._emit(type=pb.ATTACH_EVENT_TYPE_OUTPUT, payload=echo)
            for block in self.thinking:
                self._emit(type=pb.ATTACH_EVENT_TYPE_THINKING, thinking_text=block)
                time.sleep(0.05)
            if self.reply_delay:
                time.sleep(self.reply_delay)
            if self.reply:
                self._emit(type=pb.ATTACH_EVENT_TYPE_OUTPUT, payload=self.reply)
            if self.exit_after_reply:
                self._emit(type=pb.ATTACH_EVENT_TYPE_SESSION_EXIT, exit_code=0)

        threading.Thread(target=respond, daemon=True).start()

    def AttachSession(self, request, context):
        # The real server sends ATTACHED on attach and only then replays
        # buffered events, so it always arrives first even when the provider
        # already produced output (a non-interactive provider does).
        if self.attach_error:
            yield pb.AttachSessionEvent(type=pb.ATTACH_EVENT_TYPE_ERROR, error=self.attach_error)
            return
        yield pb.AttachSessionEvent(
            type=pb.ATTACH_EVENT_TYPE_ATTACHED, session_id=request.session_id
        )
        while context.is_active():
            try:
                event = self._events.get(timeout=0.1)
            except queue.Empty:
                continue
            if event is None:
                return
            yield event

    def WriteInput(self, request, context):
        self.inputs.append(request.data)
        self._respond_async(request.data)
        return pb.WriteInputResponse(accepted=True, bytes_written=len(request.data))

    def StopSession(self, request, context):
        self.stopped.append(request)
        self._events.put(None)
        return pb.StopSessionResponse(status=pb.SESSION_STATUS_STOPPED)


class FakeBridgeServer:
    """Runs a FakeBridge on a unix socket in a throwaway state directory."""

    def __init__(self, service: FakeBridge | None = None):
        self.service = service or FakeBridge()
        self._tmp = tempfile.TemporaryDirectory(prefix="bosun-fakebridge-")
        self.state_dir = Path(self._tmp.name)
        self.socket = self.state_dir / "server.sock"
        self._server = grpc.server(futures.ThreadPoolExecutor(max_workers=8))
        pb_grpc.add_BridgeServiceServicer_to_server(self.service, self._server)
        self._server.add_insecure_port(f"unix://{self.socket}")

    def __enter__(self) -> "FakeBridgeServer":
        self._server.start()
        return self

    def __exit__(self, *exc) -> None:
        self._server.stop(grace=0)
        self._tmp.cleanup()

    @property
    def target(self) -> str:
        return f"unix://{self.socket}"
