"""Run one review as a bridgectl agent session.

Bosun talks to the bridge server directly (see bosun/client.py) rather than
piping a prompt into ``bridgectl session start --no-tty``. The CLI forwards
stdin into the session and treats EOF as the operator leaving, so a piped
prompt force-stops the agent before it can answer. Driving the API instead
means the prompt is delivered with ``WriteInput`` and the session is ended
deliberately with ``StopSession``.

The provider still writes to a PTY, so output arrives as terminal bytes with
ANSI sequences and an echo of the prompt; both are cleaned up here.
"""

import os
import re
import threading
import time

import grpc

from .client import BridgeClient, BridgeError, new_session_id
from .client import pb
from .logs import get_logger

log = get_logger("bosun.session")

# CSI/OSC/single-character escapes emitted by provider TUIs.
_ANSI = re.compile(r"\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]")

DEFAULT_IDLE_SECONDS = int(os.getenv("BOSUN_REVIEW_IDLE_SECONDS", "90"))
# One knob for the whole review; the Job deadline is derived from it.
DEFAULT_MAX_SECONDS = int(os.getenv("BOSUN_REVIEW_TIMEOUT_SECONDS", "1800"))
# Stream-JSON providers emit reasoning separately from their answer. It is
# useful in logs but must not land in the pull-request comment.
INCLUDE_THINKING = os.getenv("BOSUN_INCLUDE_THINKING", "false").lower() == "true"

# Providers that render a full-screen terminal UI. Their PTY stream is screen
# repaints, not an answer, so scraping it yields box-drawing noise rather than a
# review. Stream-JSON providers (opencode) emit structured text instead.
TUI_PROVIDERS = frozenset(
    p.strip() for p in os.getenv("BOSUN_TUI_PROVIDERS", "codex,gemini").split(",") if p.strip()
)
_BOX_DRAWING = set("─│╭╮╰╯┌┐└┘├┤┬┴┼━┃▌▐█▀▄")

# Escape hatch: post whatever the provider produced, even if it is screen
# repaints. Useful when inspecting a TUI provider's raw stream by hand.
ALLOW_TUI_OUTPUT = os.getenv("BOSUN_ALLOW_TUI_OUTPUT", "false").lower() == "true"


class ReviewFailed(RuntimeError):
    pass


def clean_output(raw: str) -> str:
    """Strip terminal control sequences and normalise line endings."""
    text = _ANSI.sub("", raw)
    text = text.replace("\r\n", "\n").replace("\r", "\n")
    text = "".join(ch for ch in text if ch == "\n" or ch == "\t" or ord(ch) >= 32)
    return "\n".join(line.rstrip() for line in text.split("\n")).strip()


def strip_prompt_echo(output: str, prompt: str) -> str:
    """Remove the prompt the provider echoed back before its answer."""
    echoed = [line.strip() for line in prompt.split("\n") if line.strip()]
    if not echoed:
        return output
    lines = output.split("\n")
    index = 0
    remaining = set(echoed)
    while index < len(lines):
        candidate = lines[index].strip()
        if not candidate:
            index += 1
            continue
        if candidate in remaining:
            remaining.discard(candidate)
            index += 1
            continue
        break
    # Only treat it as an echo if most of the prompt was actually repeated back.
    if len(remaining) > len(echoed) // 2:
        return output
    return "\n".join(lines[index:]).strip()


def _require_provider(client: BridgeClient, provider: str) -> None:
    """Fail early with a useful message rather than an opaque StartSession error.

    A provider reports unavailable when its CLI is missing or its credential is
    not set — for example `codex` without OPENAI_API_KEY.
    """
    try:
        providers = {p.provider: p for p in client.list_providers()}
    except Exception:  # noqa: BLE001 - preflight only; StartSession reports the real error
        return
    info = providers.get(provider)
    if info is None:
        known = ", ".join(sorted(providers)) or "none"
        raise ReviewFailed(f"unknown provider {provider!r}; bridgectl offers: {known}")
    if not info.available:
        available = ", ".join(sorted(p for p, i in providers.items() if i.available)) or "none"
        raise ReviewFailed(
            f"provider {provider!r} is not available in this image "
            f"(is its credential set?); available: {available}"
        )


def looks_like_terminal_ui(text: str, threshold: float = 0.02) -> bool:
    """True when `text` is a repainted terminal UI rather than prose.

    Full-screen TUIs draw frames out of box characters and rewrite them on every
    update, so even a short answer arrives buried in thousands of them.
    """
    if not text:
        return False
    boxes = sum(1 for ch in text if ch in _BOX_DRAWING)
    return boxes / len(text) > threshold


class _Collector:
    """Consumes the attach stream on a background thread."""

    def __init__(self, stream, client_id: str):
        self.stream = stream
        self.client_id = client_id
        self.chunks: list[bytes] = []
        self.thinking: list[str] = []
        self.error: str | None = None
        self.exit_code: int | None = None
        self.attached = threading.Event()
        self.finished = threading.Event()
        self.last_output = time.monotonic()
        self.saw_output = threading.Event()
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self):
        self._thread.start()

    def _run(self):
        try:
            for event in self.stream:
                if event.type == pb.ATTACH_EVENT_TYPE_ATTACHED:
                    self.attached.set()
                elif event.type == pb.ATTACH_EVENT_TYPE_OUTPUT:
                    self.chunks.append(event.payload)
                    self.last_output = time.monotonic()
                    self.saw_output.set()
                elif event.type == pb.ATTACH_EVENT_TYPE_THINKING:
                    # Stream-JSON providers (e.g. claude --output-format
                    # stream-json) emit reasoning before the answer. Count it as
                    # activity so a long thinking phase is not mistaken for an
                    # idle session, but keep it out of the review body.
                    self.thinking.append(event.thinking_text)
                    self.last_output = time.monotonic()
                    log.debug(
                        "agent thinking",
                        extra={"context": {"chars": len(event.thinking_text)}},
                    )
                elif event.type == pb.ATTACH_EVENT_TYPE_SESSION_EXIT:
                    self.exit_code = event.exit_code
                    break
                elif event.type == pb.ATTACH_EVENT_TYPE_ERROR:
                    self.error = event.error
                    break
                elif event.type == pb.ATTACH_EVENT_TYPE_REPLAY_GAP:
                    log.warning("replay gap: oldest=%s last=%s", event.oldest_seq, event.last_seq)
        except grpc.RpcError as exc:
            # Cancelled is expected: the stream is torn down once we stop.
            if exc.code() not in (grpc.StatusCode.CANCELLED,):
                self.error = self.error or str(exc.details() or exc)
        finally:
            self.finished.set()

    def text(self) -> str:
        return b"".join(self.chunks).decode("utf-8", "replace")

    def thinking_text(self) -> str:
        return "\n".join(t for t in self.thinking if t).strip()


def run_review(
    workspace,
    provider: str,
    prompt: str,
    project: str = "bosun-review",
    idle_seconds: int = DEFAULT_IDLE_SECONDS,
    max_seconds: int = DEFAULT_MAX_SECONDS,
    client: BridgeClient | None = None,
    attach_timeout: int = 60,
    env: dict | None = None,
) -> str:
    """Start a session, send the prompt, and return the agent's cleaned output.

    Output events carry the answer; THINKING events carry a stream-JSON
    provider's reasoning and are kept separate.

    `env` is handed to the bridge server if Bosun has to start one; the server
    forks the provider, so that environment is what the agent ends up seeing.
    """
    owned = client is None
    if client is None:
        try:
            client = BridgeClient.connect(env=env)
        except BridgeError as exc:
            raise ReviewFailed(str(exc)) from exc

    session_id = new_session_id()
    client_id = new_session_id()
    collector = None
    try:
        if provider in TUI_PROVIDERS:
            log.warning(
                "provider renders a terminal UI; its output is unlikely to be a usable review",
                extra={"context": {"provider": provider}},
            )
        _require_provider(client, provider)
        client.start_session(session_id, project, str(workspace), provider)
        collector = _Collector(client.attach(session_id, client_id), client_id)
        collector.start()

        if not collector.attached.wait(timeout=attach_timeout):
            if collector.error:
                raise ReviewFailed(collector.error)
            raise ReviewFailed(f"did not attach to session within {attach_timeout}s")

        log.info(
            "session started",
            extra={"context": {"session": session_id, "provider": provider,
                               "workspace": str(workspace), "timeout": max_seconds}},
        )
        client.write_input(session_id, client_id, prompt.encode() + b"\n")

        deadline = time.monotonic() + max_seconds
        while not collector.finished.is_set():
            now = time.monotonic()
            if now > deadline:
                log.warning("review exceeded %ss; stopping session", max_seconds)
                break
            if collector.saw_output.is_set() and now - collector.last_output > idle_seconds:
                break
            time.sleep(0.5)

        # Ending the session deliberately, rather than by closing stdin, is the
        # whole reason for using the API directly.
        client.stop_session(session_id)
        collector.finished.wait(timeout=15)

        if collector.error and not collector.chunks:
            raise ReviewFailed(collector.error[-4000:])

        output = strip_prompt_echo(clean_output(collector.text()), prompt)
        thinking = collector.thinking_text()
        if not output and thinking:
            # A stream-JSON provider that only produced reasoning still failed to
            # answer, but the reasoning explains why.
            log.warning("agent produced reasoning but no answer",
                        extra={"context": {"thinking_chars": len(thinking)}})
        if not output:
            detail = collector.error or "the agent session produced no output"
            raise ReviewFailed(detail[-4000:])

        if looks_like_terminal_ui(output) and ALLOW_TUI_OUTPUT:
            log.warning(
                "posting terminal UI output because BOSUN_ALLOW_TUI_OUTPUT is set",
                extra={"context": {"provider": provider, "chars": len(output)}},
            )
        elif looks_like_terminal_ui(output):
            raise ReviewFailed(
                f"provider {provider!r} returned terminal UI output rather than a review. "
                f"It renders a full-screen interface, so its PTY stream is screen repaints. "
                f"Use a stream-JSON provider (opencode) for reviews, "
                f"or set BOSUN_ALLOW_TUI_OUTPUT=true to keep it anyway."
            )

        log.info(
            "review complete",
            extra={"context": {"provider": provider, "chars": len(output),
                               "thinking_chars": len(thinking),
                               "exit_code": collector.exit_code if collector.exit_code is not None else ""}},
        )
        if thinking and INCLUDE_THINKING:
            return f"{output}\n\n<details><summary>Agent reasoning</summary>\n\n{thinking}\n\n</details>"
        return output
    except grpc.RpcError as exc:
        raise ReviewFailed(f"bridge RPC failed: {exc.details() or exc}") from exc
    finally:
        if collector is not None and not collector.finished.is_set():
            client.stop_session(session_id, force=True)
        if owned:
            client.close()
