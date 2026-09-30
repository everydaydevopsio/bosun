#!/usr/bin/env bash
# Copy a checkout, including linked worktrees, into a self-contained snapshot.
set -euo pipefail
[ "$#" -eq 2 ] || { echo "usage: $0 <repository> <empty destination>" >&2; exit 2; }
REPO="$(cd "$1" && pwd)"
DEST="$2"
mkdir -p "$DEST"
[ -z "$(ls -A "$DEST")" ] || { echo "snapshot destination must be empty" >&2; exit 2; }
# A mirror preserves default-branch and remote refs. Copy objects rather than
# hardlinking them, and detach from any external object alternates.
git clone --quiet --mirror --no-hardlinks --dissociate -- "$REPO" "$DEST/.git"
git -C "$DEST" config core.bare false
git -C "$DEST" update-ref --no-deref HEAD "$(git -C "$REPO" rev-parse HEAD)"
if HEAD_REF="$(git -C "$REPO" symbolic-ref -q HEAD)"; then
  git -C "$DEST" symbolic-ref HEAD "$HEAD_REF"
fi
if DEFAULT_REF="$(git -C "$REPO" symbolic-ref -q refs/remotes/origin/HEAD)"; then
  git -C "$DEST" symbolic-ref refs/remotes/origin/HEAD "$DEFAULT_REF"
fi
# Keep the source worktree's index (including staged changes), not the index
# belonging to the main checkout. Local cloning copied its objects as well.
INDEX="$(git -C "$REPO" rev-parse --path-format=absolute --git-path index)"
if [ -f "$INDEX" ]; then
  cp "$INDEX" "$DEST/.git/index"
  COMMON="$(git -C "$REPO" rev-parse --path-format=absolute --git-common-dir)"
  for shared in "$(dirname "$INDEX")"/sharedindex.* "$COMMON"/sharedindex.*; do
    [ ! -f "$shared" ] || cp "$shared" "$DEST/.git/"
  done
fi
# Overlay exactly the source files; no checkout step can resurrect deletions.
tar -C "$REPO" --exclude='./.git' --exclude='./.env' -cf - . | tar -C "$DEST" -xf -
