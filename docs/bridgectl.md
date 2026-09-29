# Working with bridgectl

Bosun runs its Go reviewer inside the bridgectl image and drives the local
`BridgeService` gRPC API through `internal/review/bridge.go`.

## Base image pinning

The Dockerfile pins `ghcr.io/orchael/bridgectl:v1.3.0`; the image publishes
version tags, not `latest`. Override the pin with `BRIDGECTL_VERSION` for
`make build` / `make kind-up`, or `--build-arg BRIDGECTL_VERSION=vX.Y.Z` for
Docker. Keep the pin aligned with the version used in your environment.

## Driving a session

The reviewer starts `bridgectl server start --config
/app/bosun/config/bridge-bosun.yaml` when no local server is present. That
configuration adds Bosun's `codex-exec` provider. Provider processes inherit
AI credentials but not Bosun's GitHub token or App credentials.

A review starts a session, attaches to its output stream, collects output,
and stops the session during cleanup. The review succeeds only after a
successful `SESSION_EXIT` with nonempty output. Stream errors, nonzero exits,
and deadlines fail the review rather than publishing partial output.

For `codex-exec`, Bosun passes the prompt as `agent_opts["arg:prompt"]`.
Other providers receive it through `WriteInput` after the writer attaches.
The configured review deadline covers authentication, checkout, agent execution,
and publishing.

Do not pipe a prompt into `bridgectl session start --no-tty`: stdin EOF can
stop that session before the provider finishes. Bosun owns the session through
gRPC instead.

## Choosing a provider

`codex-exec` is the default for the Go controller, Helm chart, and local scripts.
The packaged `codex` provider is an interactive terminal UI and is unsuitable
for unattended reviews. `opencode` is another stream-JSON provider, but its
session must still produce a successful exit before Bosun considers it complete.

Bosun's headless provider runs the packaged executable at
`/app/node_modules/@openai/codex/bin/codex.js` with `exec`, `--color never`, and
`--sandbox read-only`. Its shell wrapper forwards the prompt as one quoted
argument and redirects stdin from `/dev/null`: Codex otherwise waits for EOF
on the pipe that bridgectl keeps open.

`stream_json: true` disables PTY allocation. Plain stdout lines are forwarded
as output events, and Bosun preserves their line boundaries in the review.

`internal/review/credentials.go` writes `CODEX_AUTH` to `$CODEX_HOME/auth.json`
(or `~/.codex/auth.json`) with mode 0600. It also supports
`CLAUDE_CREDENTIALS` for file-based Claude authentication. Raw file credentials
are removed from the child environment after materialization.

## Regenerating the stubs

`proto/bridge/v1/bridge.proto` is vendored from bridgectl. After updating it:

```bash
make proto
```

This requires `protoc` and the `protoc-gen-go` and `protoc-gen-go-grpc` plugins
in `GOPATH/bin`. Generated Go bindings live in `internal/bridgev1/` and are
checked into Git, so normal builds and tests do not require regeneration.
