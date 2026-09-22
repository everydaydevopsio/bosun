#!/usr/bin/env bash
set -euo pipefail
CLUSTER="${BOSUN_KIND_CLUSTER:-bosun}"
IMAGE="${BOSUN_DEV_IMAGE:-bosun:dev}"
PROVIDER="${BOSUN_REVIEW_PROVIDER:-codex}"

command -v kind >/dev/null || { echo "kind is required" >&2; exit 2; }
command -v kubectl >/dev/null || { echo "kubectl is required" >&2; exit 2; }
command -v helm >/dev/null || { echo "helm is required" >&2; exit 2; }
command -v docker >/dev/null || { echo "docker is required" >&2; exit 2; }

mkdir -p /tmp/bosun-repos
kind get clusters | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --config kind-config.yaml

docker build -t "$IMAGE" .
kind load docker-image "$IMAGE" --name "$CLUSTER"

kubectl create namespace bosun --dry-run=client -o yaml | kubectl apply -f -
if [ "$PROVIDER" = "claude" ]; then
  : "${CLAUDE_CODE_OAUTH_TOKEN:?set CLAUDE_CODE_OAUTH_TOKEN}"
  kubectl -n bosun create secret generic bosun-ai --from-literal=claude-code-oauth-token="$CLAUDE_CODE_OAUTH_TOKEN" --dry-run=client -o yaml | kubectl apply -f -
else
  : "${OPENAI_API_KEY:?set OPENAI_API_KEY}"
  kubectl -n bosun create secret generic bosun-ai --from-literal=openai-api-key="$OPENAI_API_KEY" --dry-run=client -o yaml | kubectl apply -f -
fi

helm upgrade --install bosun ./charts/bosun -n bosun   --set image.repository=bosun   --set image.tag=dev   --set image.pullPolicy=IfNotPresent   --set review.image="$IMAGE"   --set review.provider="$PROVIDER"   --set development.enabled=true

echo "Bosun Kind environment is ready."
echo "Trigger a review with: ./scripts/review-local.sh /path/to/git/repo"
