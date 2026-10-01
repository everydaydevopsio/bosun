#!/usr/bin/env bash
set -euo pipefail
# Compatibility entrypoint; native CLI owns snapshots, monitoring, and cleanup.
HERE="$(cd "$(dirname "$0")" && pwd)"
if [ -n "${BOSUN_BIN:-}" ]; then
  exec "$BOSUN_BIN" review "$@"
fi
if command -v bosun >/dev/null 2>&1; then
  exec bosun review "$@"
fi
# Preserve the caller's working directory when building from the source tree.
BIN="$(mktemp /tmp/bosun-cli.XXXXXXXX)"
trap 'rm -f "$BIN"' EXIT
(cd "$HERE/.." && go build -o "$BIN" ./cmd/bosun)
"$BIN" review "$@"
