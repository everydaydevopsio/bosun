#!/usr/bin/env bash
set -euo pipefail

# Exercise the built worker's pinned bridgectl binary and shipped config.
# A second argument mounts a candidate config over a released image for local
# diagnosis; CI passes only the freshly built image.
image="${1:?usage: smoke-bridge-path.sh IMAGE [CONFIG_FILE]}"
mount_args=()
if [ -n "${2:-}" ]; then
  mount_args=(--mount "type=bind,src=$(realpath "$2"),dst=/app/bosun/config/bridge-bosun.yaml,readonly")
fi

docker run --rm -i --tmpfs /repos:mode=1777 "${mount_args[@]}" --entrypoint sh "$image" -s <<'IN_CONTAINER'
set -eu
mkdir /repos/review.contract /tmp/bosun-outside-contract
bridgectl server start --config /app/bosun/config/bridge-bosun.yaml >/tmp/bridge.log 2>&1 &
server_pid=$!
trap 'kill "$server_pid" 2>/dev/null || true' EXIT

ready=0
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
  if [ -S "$HOME/.config/bridgectl/server.sock" ]; then ready=1; break; fi
  sleep 0.25
done
if [ "$ready" -ne 1 ]; then
  echo "SMOKE TEST FAILED: bridgectl did not start" >&2
  cat /tmp/bridge.log >&2
  exit 1
fi

# stdin EOF stops echo sessions on this bridgectl version, so session-start
# exits nonzero. The server's session list proves StartSession accepted the path.
timeout 10s bridgectl session start /repos/review.contract --provider echo --no-tty </dev/null >/tmp/allowed.log 2>&1 || true
if grep -q 'not under any allowed path' /tmp/allowed.log || ! bridgectl session list | grep -q 'echo'; then
  echo "SMOKE TEST FAILED: /repos snapshot session was rejected" >&2
  cat /tmp/allowed.log >&2
  exit 1
fi

timeout 10s bridgectl session start /tmp/bosun-outside-contract --provider echo --no-tty </dev/null >/tmp/denied.log 2>&1 || true
if ! grep -q 'not under any allowed path' /tmp/denied.log; then
  echo "SMOKE TEST FAILED: outside session path was accepted" >&2
  cat /tmp/denied.log >&2
  exit 1
fi
echo "SMOKE TEST PASSED: /repos accepted, outside path denied"
IN_CONTAINER
