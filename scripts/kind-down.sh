#!/usr/bin/env bash
set -euo pipefail
kind delete cluster --name "${BOSUN_KIND_CLUSTER:-bosun}"
rm -rf /tmp/bosun-repos
