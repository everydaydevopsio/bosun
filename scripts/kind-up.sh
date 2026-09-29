#!/usr/bin/env bash
set -euo pipefail

CLUSTER="${BOSUN_KIND_CLUSTER:-bosun}"
CONTEXT="kind-$CLUSTER"
IMAGE="${BOSUN_DEV_IMAGE:-bosun:dev}"
PROVIDER="${BOSUN_REVIEW_PROVIDER:-codex-exec}"

command -v kind >/dev/null || { echo "kind is required" >&2; exit 2; }
command -v kubectl >/dev/null || { echo "kubectl is required" >&2; exit 2; }
command -v helm >/dev/null || { echo "helm is required" >&2; exit 2; }
command -v docker >/dev/null || { echo "docker is required" >&2; exit 2; }

mkdir -p /tmp/bosun-repos
kind get clusters | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --config kind-config.yaml

# BOSUN_SKIP_BUILD=1 reuses an image already loaded into the cluster.
if [ "${BOSUN_SKIP_BUILD:-}" != "1" ]; then
  docker build --build-arg BRIDGECTL_VERSION="${BRIDGECTL_VERSION:-v1.3.0}" -t "$IMAGE" .
  kind load docker-image "$IMAGE" --name "$CLUSTER"
fi

kubectl --context "$CONTEXT" create namespace bosun --dry-run=client -o yaml | kubectl --context "$CONTEXT" apply -f -

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

if [ "${#SECRET_ARGS[@]}" -eq 0 ]; then
  echo "No provider credentials found in the environment." >&2
  echo "Expected one of OPENAI_API_KEY, CODEX_AUTH, CLAUDE_CODE_OAUTH_TOKEN," >&2
  echo "CLAUDE_CREDENTIALS, ANTHROPIC_API_KEY or GEMINI_API_KEY" >&2
  echo "Export credentials before running this script; see docs/local-development.md." >&2
  exit 2
fi

kubectl --context "$CONTEXT" -n bosun create secret generic bosun-ai "${SECRET_ARGS[@]}" \
  --dry-run=client -o yaml | kubectl --context "$CONTEXT" apply -f -

case "$PROVIDER" in
  codex|codex-exec)
    [ -n "${OPENAI_API_KEY:-}${CODEX_AUTH:-}" ] || {
      echo "provider $PROVIDER needs OPENAI_API_KEY or CODEX_AUTH" >&2; exit 2; } ;;
  opencode)
    [ -n "${OPENAI_API_KEY:-}" ] || { echo "provider opencode needs OPENAI_API_KEY" >&2; exit 2; } ;;
  claude)
    [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}${ANTHROPIC_API_KEY:-}${CLAUDE_CREDENTIALS:-}" ] || {
      echo "provider claude needs CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY or CLAUDE_CREDENTIALS" >&2; exit 2; } ;;
  gemini)
    [ -n "${GEMINI_API_KEY:-}" ] || { echo "provider gemini needs GEMINI_API_KEY" >&2; exit 2; } ;;
esac

helm --kube-context "$CONTEXT" upgrade --install bosun ./charts/bosun -n bosun \
  --set image.repository=bosun \
  --set image.tag=dev \
  --set image.pullPolicy=IfNotPresent \
  --set review.image="$IMAGE" \
  --set review.provider="$PROVIDER" \
  --set development.enabled=true

# A reused development image tag does not change the Deployment template.
kubectl --context "$CONTEXT" -n bosun rollout restart deployment/bosun-bosun
kubectl --context "$CONTEXT" -n bosun rollout status deployment/bosun-bosun --timeout=180s

echo "Bosun Kind environment is ready (provider: $PROVIDER)."
echo "Trigger a review with: ./scripts/review-local.sh /path/to/git/repo"
