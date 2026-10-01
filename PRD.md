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
