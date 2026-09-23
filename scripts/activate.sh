# shellcheck shell=bash
# Load the Bosun development environment into the current shell.
#
#   source ./scripts/activate.sh
#
# Sourced, not executed: it exports the agent credentials written by
# `make setup` and activates the Python virtualenv.

# $0 is the sourced path in zsh; BASH_SOURCE is the bash equivalent.
_bosun_root="${BASH_SOURCE[0]:-$0}"
_bosun_root="$(cd "$(dirname "$_bosun_root")/.." && pwd)"

if [ -f "$_bosun_root/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$_bosun_root/.env"
  set +a
  echo "Loaded agent credentials from .env"
else
  echo "No .env found; run 'make setup' first." >&2
fi

if [ -f "$_bosun_root/.venv/bin/activate" ]; then
  # shellcheck disable=SC1091
  . "$_bosun_root/.venv/bin/activate"
  echo "Activated .venv ($(python --version 2>&1))"
else
  echo "No .venv found; run 'make setup' first." >&2
fi

# Credentials are already in the environment, so scripts need not re-fetch them.
export BOSUN_SECRETS_LOADED=1
export BRIDGECTL_VERSION="${BRIDGECTL_VERSION:-v1.3.0}"
export PYTHONPATH="$_bosun_root:${PYTHONPATH:-}"
unset _bosun_root
