#!/usr/bin/env bash
set -euo pipefail

CLUSTER="${BOSUN_KIND_CLUSTER:-bosun}"
CONTEXT="kind-$CLUSTER"
IMAGE="${BOSUN_DEV_IMAGE:-bosun:dev}"
# The controller Deployment must request the image Kind actually loaded. Split
# on the tag separator only in the final path segment, so a registry port such
# as localhost:5000/bosun is not mistaken for a tag.
case "${IMAGE##*/}" in
  *:*) IMAGE_REPOSITORY="${IMAGE%:*}"; IMAGE_TAG="${IMAGE##*:}" ;;
  *)   IMAGE_REPOSITORY="$IMAGE";      IMAGE_TAG="latest" ;;
esac
PROVIDER="${BOSUN_REVIEW_PROVIDER:-codex-bosun}"

command -v kind >/dev/null || { echo "kind is required" >&2; exit 2; }
command -v kubectl >/dev/null || { echo "kubectl is required" >&2; exit 2; }
command -v helm >/dev/null || { echo "helm is required" >&2; exit 2; }
command -v docker >/dev/null || { echo "docker is required" >&2; exit 2; }

# Publish whichever provider credentials are present. Each is optional in the
# Job spec, so a partial secret is fine as long as the selected provider's key
# is there.
SECRET_ARGS=()
[ -n "${OPENAI_API_KEY:-}" ] && SECRET_ARGS+=(--from-literal=openai-api-key="$OPENAI_API_KEY")
[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && SECRET_ARGS+=(--from-literal=claude-code-oauth-token="$CLAUDE_CODE_OAUTH_TOKEN")
[ -n "${ANTHROPIC_API_KEY:-}" ] && SECRET_ARGS+=(--from-literal=anthropic-api-key="$ANTHROPIC_API_KEY")
[ -n "${GEMINI_API_KEY:-}" ] && SECRET_ARGS+=(--from-literal=gemini-api-key="$GEMINI_API_KEY")
# codex in ChatGPT OAuth mode authenticates from ~/.codex/auth.json, not a key.
[ -n "${CODEX_AUTH:-}" ] && SECRET_ARGS+=(--from-literal=codex-auth="$CODEX_AUTH")
[ -n "${CLAUDE_CREDENTIALS:-}" ] && SECRET_ARGS+=(--from-literal=claude-credentials="$CLAUDE_CREDENTIALS")

# The CLI owns the cluster, the namespace, and credential discovery, so a
# contributor and a released-CLI user go through one implementation. With no
# exported credentials it falls back to this machine's codex/claude sign-in and
# fails with its own message when the selected provider has neither.
CLI_ARGS=(--cluster "$CLUSTER" --namespace bosun --provider "$PROVIDER")
if [ "${#SECRET_ARGS[@]}" -eq 0 ]; then
  CLI_ARGS+=(--local-credentials)
fi
go run ./cmd/bosun up "${CLI_ARGS[@]}" >/dev/null

if [ "${#SECRET_ARGS[@]}" -gt 0 ]; then
  kubectl --context "$CONTEXT" -n bosun create secret generic bosun-ai "${SECRET_ARGS[@]}" \
    --dry-run=client -o yaml | kubectl --context "$CONTEXT" apply -f -
fi

# Exported credentials are validated here; discovered ones by the CLI above.
if [ "${#SECRET_ARGS[@]}" -gt 0 ]; then
case "$PROVIDER" in
  codex|codex-bosun)
    [ -n "${OPENAI_API_KEY:-}${CODEX_AUTH:-}" ] || {
      echo "provider $PROVIDER needs OPENAI_API_KEY or CODEX_AUTH" >&2; exit 2; } ;;
  opencode)
    [ -n "${OPENAI_API_KEY:-}" ] || { echo "provider opencode needs OPENAI_API_KEY" >&2; exit 2; } ;;
  claude|claude-bosun)
    [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}${ANTHROPIC_API_KEY:-}${CLAUDE_CREDENTIALS:-}" ] || {
      echo "provider $PROVIDER needs CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY or CLAUDE_CREDENTIALS" >&2; exit 2; } ;;
  gemini)
    [ -n "${GEMINI_API_KEY:-}" ] || { echo "provider gemini needs GEMINI_API_KEY" >&2; exit 2; } ;;
esac
fi

# BOSUN_SKIP_BUILD=1 reuses an image already loaded into the cluster. This runs
# after the cluster exists, because `kind load` needs a node to load into.
if [ "${BOSUN_SKIP_BUILD:-}" != "1" ]; then
  docker build --build-arg BRIDGECTL_VERSION="${BRIDGECTL_VERSION:-v1.4.1}" -t "$IMAGE" .
  kind load docker-image "$IMAGE" --name "$CLUSTER"
fi

helm --kube-context "$CONTEXT" upgrade --install bosun ./charts/bosun -n bosun \
  --set image.repository="$IMAGE_REPOSITORY" \
  --set image.tag="$IMAGE_TAG" \
  --set image.pullPolicy=IfNotPresent \
  --set review.image="$IMAGE" \
  --set review.provider="$PROVIDER" \
  --set development.enabled=true

# A reused development image tag does not change the Deployment template.
kubectl --context "$CONTEXT" -n bosun rollout restart deployment/bosun-bosun
kubectl --context "$CONTEXT" -n bosun rollout status deployment/bosun-bosun --timeout=180s

echo "Bosun Kind environment is ready (provider: $PROVIDER)."
echo "Trigger a review with: ./scripts/review-local.sh /path/to/git/repo"
