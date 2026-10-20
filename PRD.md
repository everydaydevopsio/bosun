# Bosun Go Migration

## GO-1: Preserve the Bosun webhook contract

Bosun must expose `GET /healthz` returning `{"ok":true}` and
`POST /webhooks/github`. The webhook endpoint must reject unsigned or malformed
payloads, accept the existing branch, pull-request, and authorized comment
triggers, preserve delivery de-duplication and capacity semantics, and retain
the currently documented environment-variable and Helm-value interface.

## GO-2: Preserve reviewer behavior

The reviewer must create the same Kubernetes Job shape, obtain a scoped GitHub
token, clone the requested revision without putting that token in Git metadata,
run a local bridgectl gRPC session, redact credentials, and post the resulting
review to the pull request when applicable. Local Kind reviews must include
uncommitted changes and print the result without modifying the source checkout.
For a local pull-request review, the comparison base must be the exact commit
reported by the target upstream repository. If that commit cannot be fetched
and verified, submission must stop. The CLI must report the resolved base and
merge base so a reviewer can audit the comparison.
When a provider fails, the review status must retain a safe diagnostic that
classifies known authentication failures and gives the local user a concrete
credential refresh step without exposing provider output or credentials.
When `bosun up` checks a local review provider, it must inspect the selected
credential in the Kind Secret. With `--local-credentials`, it must confirm a
headless provider request succeeds before copying the local credential.

## GO-4: Publish findings where they can be acted on

A hosted review with a pull-request number must end in exactly one terminal
publication: a review, or a failure comment. Never both, never neither.

Findings must be published as a pull-request review with one inline comment per
finding anchored to the diff, so replies and thread resolution work without a
consumer reconstructing state from prose. Findings that cannot be anchored --
no machine-readable findings block, a path outside the diff, or a position
GitHub rejects -- must still be published as text rather than lost.

Every publication must name the ref and commit it reviewed, so a consumer can
correlate output to a push rather than to a timestamp.

A terminal failure must be published to the pull request with a redacted
reason naming the stage that failed, and must be distinguishable from a review
by its marker line. Marker lines are a public interface; changing one is a
breaking change for consuming agents.

## GO-3: Replace the Python runtime

The production image, controller, reviewer, protobuf bindings, and automated
tests must be Go. Python requirements and Python runtime entrypoints are
removed. `make test`, `make lint`, `make build`, `make kind-up`, and `make
review` remain the supported operator interface.

## Acceptance criteria

1. `go test ./...` passes and covers webhook trigger/signature, job rendering,
   token isolation, clone selection, and review-session failure paths.
2. `make test`, `make lint`, and `make build` use Go tooling and pass from a
   clean supported developer environment.
3. Helm renders a Deployment that starts the Go controller and local review
   Jobs that start the Go reviewer.
4. The old Python source and dependency manifests no longer participate in the
   build or runtime image.
5. A rollback consists of deploying the preceding image/chart revision; the
   public configuration and Kubernetes object names are unchanged.
6. A pull-request review publishes inline comments for anchorable findings,
   falls back to a body-only publication when GitHub rejects a position, and
   publishes a failure comment when the review does not complete.
