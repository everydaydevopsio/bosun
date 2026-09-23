#!/usr/bin/env bash
# Run a command with the agent provider keys loaded from AWS Secrets Manager.
#
#   ./scripts/with-secrets.sh ./scripts/kind-up.sh
#
# Secrets come from BOSUN_AWS_SECRET (default /ai-desktops/markcallen/agents)
# via env-secrets, which injects every key in the secret as an environment
# variable for the child process only — nothing is written to disk.
#
# Set BOSUN_SKIP_SECRETS=1 to use credentials already in your environment.
set -euo pipefail

SECRET="${BOSUN_AWS_SECRET:-/ai-desktops/markcallen/agents}"

[ "$#" -gt 0 ] || { echo "usage: $0 <command> [args...]" >&2; exit 2; }

# Already inside an env-secrets wrapper, or explicitly skipped.
if [ -n "${BOSUN_SECRETS_LOADED:-}" ] || [ "${BOSUN_SKIP_SECRETS:-}" = "1" ]; then
  exec "$@"
fi

if ! command -v env-secrets >/dev/null 2>&1; then
  echo "env-secrets is not installed; run 'make deps' (or set BOSUN_SKIP_SECRETS=1)" >&2
  exit 2
fi

export BOSUN_SECRETS_LOADED=1
ARGS=(aws -s "$SECRET")
[ -n "${AWS_PROFILE:-}" ] && ARGS+=(-p "$AWS_PROFILE")
[ -n "${AWS_REGION:-}" ] && ARGS+=(-r "$AWS_REGION")

echo "Loading agent credentials from $SECRET" >&2
exec env-secrets "${ARGS[@]}" --no-shell "$@"
