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
configuration adds Bosun's `codex-bosun` and `claude-bosun` providers. Provider processes inherit
AI credentials but not Bosun's GitHub token or App credentials.

A review starts a session, attaches to its output stream, collects output,
and stops the session during cleanup. The review succeeds only after a
successful `SESSION_EXIT` with nonempty output. Stream errors, nonzero exits,
and deadlines fail the review rather than publishing partial output.

For `codex-bosun` and `claude-bosun`, Bosun passes the prompt as `agent_opts["arg:prompt"]`.
Other providers receive it through `WriteInput` after the writer attaches.
The configured review deadline covers authentication, checkout, agent execution,
and publishing.

Do not pipe a prompt into `bridgectl session start --no-tty`: stdin EOF can
stop that session before the provider finishes. Bosun owns the session through
gRPC instead.

## Choosing a provider

`codex-bosun` is the default for the Go controller, Helm chart, and local scripts.
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

## Live review observability

Bosun checks `Health` and `ListProviders` before starting a review, validates
prompt acceptance, monitors `GetSession`, and records output/activity separately
from the final review. A thinking event updates the activity timestamp without
logging its content. Replay gaps fail the review instead of accepting partial
output. Recoverable attachment failures resume the original session at the last
sequence, with a bounded retry count and a server-instance check; prompts are
not resent on reattachment. Cleanup failures are reported separately.

The host CLI watches Kubernetes and consumes versioned worker events. It does
not expose the pod's gRPC server to the host. The current vendored API does not
expose tool/file progress or a completion percentage. Plain `codex-bosun` output
only establishes activity when text arrives; silence is not proof of a hang.

## Bosun provider names

Use `codex-bosun` (the default, renamed from `codex-exec`) or `claude-bosun`.
Rebuild/reload the worker image and refresh your exported
`BOSUN_REVIEW_PROVIDER` when upgrading; the old custom name is not an alias.
The packaged provider names remain separate from these Bosun-owned definitions.

`claude-bosun` runs the packaged Claude executable with `--print`, text output,
and session persistence disabled. The prompt is a quoted argument and stdin is
closed. `--permission-mode dontAsk` and an explicit tool allowlist permit reading,
searching, and Git inspection without interactive approval; edits and arbitrary
shell/test commands are not preapproved. This tool policy is not an OS sandbox.
It accepts `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`, or materialized
`CLAUDE_CREDENTIALS`. No credential values belong in the provider YAML.

Codex reviews require `openai-api-key` or `codex-auth` in the configured AI
Kubernetes Secret. Selecting `--provider codex-bosun` does not copy credentials
from the host or provision that Secret. A Secret containing only Claude
credentials cannot authenticate Codex. The worker checks for a key or usable
saved auth before starting bridgectl and reports missing credentials explicitly.
The Codex wrapper maps `OPENAI_API_KEY` to the invocation's `CODEX_API_KEY`,
preserving an explicitly supplied `CODEX_API_KEY`.
