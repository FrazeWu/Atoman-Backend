#!/bin/sh
set -u

worktree_root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
git_common_dir=$(git -C "$worktree_root" rev-parse --path-format=absolute --git-common-dir 2>/dev/null) || exit 0
repo_root=$(dirname "$git_common_dir")

case "$repo_root" in
  */Atoman-Backend|*/Atoman-Frontend) ;;
  *) exit 0 ;;
esac

command -v codegraph >/dev/null 2>&1 || exit 0

lock_file="${TMPDIR:-/tmp}/atoman-codegraph-$(basename "$repo_root").lock"
exec 9>"$lock_file" || exit 0

if ! flock -n 9; then
  printf 'codegraph sync skipped: another sync is running for %s\n' "$repo_root" >&2
  exit 0
fi

if ! timeout 120s codegraph sync "$repo_root"; then
  printf 'codegraph sync failed for %s\n' "$repo_root" >&2
fi

exit 0
