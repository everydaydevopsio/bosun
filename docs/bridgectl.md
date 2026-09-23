# Working with bridgectl

Bosun runs every review inside the bridgectl image and drives the agent through
bridgectl's non-interactive session mode. Two upstream behaviours shape how
`bosun/session.py` is written, and one upstream packaging gap shapes the Dockerfile.

## Base image pinning

bridgectl's publish workflow tags images with the release version and a short
SHA only:

```yaml
tags: |
  type=raw,value=${{ env.RELEASE_TAG }}
  type=sha,prefix=sha-,format=short
```

The package is public, so pulls need no credentials, but there is no `latest`
tag — `FROM ghcr.io/orchael/bridgectl:latest` fails for everyone:

```
ERROR: ghcr.io/orchael/bridgectl:latest: not found
```

Bosun pins an explicit version instead, which is what you want for a
reproducible build regardless:

```dockerfile
ARG BRIDGECTL_VERSION=v1.3.0
FROM ghcr.io/orchael/bridgectl:${BRIDGECTL_VERSION}
```

Override it at build time with `--build-arg BRIDGECTL_VERSION=vX.Y.Z`, or set the
`BRIDGECTL_VERSION` repository variable for the publish workflow.

Keep this pin in step with the bridgectl release you run locally, so a review
behaves the same on your machine and in the cluster:

```bash
bridgectl --version
docker build --build-arg BRIDGECTL_VERSION=vX.Y.Z -t bosun:dev .
```

`scripts/kind-up.sh` honours the same `BRIDGECTL_VERSION` environment variable.

> The pin is `v1.3.0`. At the time of writing the newest published bridgectl
> release is `v1.2.0`, so builds fail with
> `ghcr.io/orchael/bridgectl:v1.3.0: not found` until that tag is pushed. Build
> against the current release in the meantime with
> `--build-arg BRIDGECTL_VERSION=v1.2.0`.

## Driving a session

Bosun does not shell out to the CLI. `bosun/client.py` speaks the gRPC
`BridgeService` directly, which is the same API the CLI is built on — the Go
equivalent is `pkg/bridgeclient`, used by `examples/orchestrator` upstream.

In local mode the server listens on a unix socket with no TLS:

```
$BRIDGECTL_STATE_DIR/server.sock      (default ~/.config/bridgectl/server.sock)
```

`BridgeClient.connect()` mirrors the CLI's `ensureServer()`: if no socket is
present it spawns `bridgectl server start` detached, polls for the socket, then
verifies the connection with `Health`. A review is then:

1. `ListProviders` — preflight, so an unavailable provider fails with a message
   that names the likely cause rather than an opaque `StartSession` error.
2. `StartSession` with the workspace path and provider.
3. `AttachSession` as `ATTACH_ROLE_WRITER`, consumed on a background thread.
4. `WriteInput` — **this is how the prompt is delivered**.
5. Collect `ATTACH_EVENT_TYPE_OUTPUT` payloads until `SESSION_EXIT`, an `ERROR`
   event, output goes quiet, or the hard cap elapses.
6. `StopSession`.

### Why not pipe the prompt into the CLI

`bridgectl session start --no-tty` forwards stdin into the session and treats
EOF as the operator leaving: it calls `StopSession(Force: true)` and cancels the
attach stream. Piping a prompt closes stdin as soon as it is written, which
force-stops the agent before it can answer.

That path also always exits non-zero. When stdin closes, the attach stream ends
with a gRPC `Canceled` status, and `runSessionNoTTY` only tolerates
`context.Canceled`; a gRPC status error does not unwrap to it:

```
$ echo "say hello" | bridgectl session start --no-tty --provider echo /repos/demo
say hello
session ended: rpc error: code = Canceled desc = context canceled
RC=1
```

Driving the API removes both problems: the prompt is a `WriteInput` call, and
the session ends because Bosun called `StopSession`, not because a pipe closed.
`run` is deprecated in favour of `session start`, but both share the same
`runSessionNoTTY` implementation, so switching subcommands alone fixes neither.

### Regenerating the stubs

`proto/bridge/v1/bridge.proto` is vendored from bridgectl. After updating it:

```bash
pip install -r requirements-dev.txt
./scripts/gen-proto.sh
```

Stubs land in `bosun/gen/` (git-ignored) and are generated during the Docker
build and in CI.

## Choosing a provider

This is the difference between a usable review and a wall of noise.

| Provider | Transport | Usable for reviews |
| --- | --- | --- |
| `opencode` | stream-JSON server | **yes** — structured text, emits THINKING |
| `codex` | full-screen PTY TUI | no |
| `claude` | PTY stdio | marginal |
| `gemini` | PTY | no |
| `echo` | PTY | test only |

`codex` renders a full-screen terminal interface. Its PTY stream is *screen
repaints*, not an answer, so scraping it yields box-drawing frames and repeated
prompt echoes. A verified run against a two-file repository returned 24 KB that
looked like this once ANSI codes were stripped:

```
╭───────────────────────────────────────╮│ >_ OpenAI Codex (v0.155.1)  ...
╭╭╭╭╭╭Y╭╭╭╭╭╭╭╭╭ouareBosun,anindepend╭╭╭╭╭╭╭entcodereview╭╭╭╭er╭╭╭╭╭╭
```

`bosun/session.py` therefore refuses to post output that is mostly box-drawing
characters:

```
provider 'codex' returned terminal UI output rather than a review. It renders a
full-screen interface, so its PTY stream is screen repaints. Use a stream-JSON
provider (opencode) for reviews.
```

`BOSUN_TUI_PROVIDERS` controls only the up-front warning. To keep the raw stream
anyway — useful when inspecting what a TUI provider actually emits — set
`BOSUN_ALLOW_TUI_OUTPUT=true`; the review is then posted verbatim, repaints and
all.

Upstream also defines a `claude-chat` provider — `claude --output-format
stream-json --verbose`, no PTY, emitting text and thinking deltas — but v1.3.0
does not register it, so `opencode` is the only structured provider available
today.

### Output is raw PTY text

For PTY providers, output arrives as terminal bytes, including ANSI colour
sequences, and the provider echoes the prompt back before replying.
`bosun/session.py` strips control sequences and removes one echoed copy of the
prompt. Sessions start at 200 columns so the agent does not hard-wrap the
Markdown it produces.

Stream-JSON providers skip all of that: the supervisor parses their stdout and
delivers typed events, so OUTPUT carries clean text and THINKING carries the
model's reasoning. Bosun keeps reasoning out of the pull-request comment unless
`BOSUN_INCLUDE_THINKING=true`, but counts it as activity so a long thinking
phase is not mistaken for an idle session.
