#!/usr/bin/env bash
set -euo pipefail
# Compatibility entrypoint; the native CLI owns the cluster lifecycle.
HERE="$(cd "$(dirname "$0")" && pwd)"
ARGS=(down --cluster "${BOSUN_KIND_CLUSTER:-bosun}")
if [ -n "${BOSUN_BIN:-}" ]; then
  exec "$BOSUN_BIN" "${ARGS[@]}"
fi
if command -v bosun >/dev/null 2>&1; then
  exec bosun "${ARGS[@]}"
fi
cd "$HERE/.."
exec go run ./cmd/bosun "${ARGS[@]}"
