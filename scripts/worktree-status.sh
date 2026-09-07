#!/bin/sh
set -u

worktree_root=$(git rev-parse --show-toplevel) || exit 1
git_common_dir=$(git -C "$worktree_root" rev-parse --path-format=absolute --git-common-dir) || exit 1
repo_root=$(dirname "$git_common_dir")
main_ref=origin/main

if ! git -C "$repo_root" rev-parse --verify "$main_ref" >/dev/null 2>&1; then
  printf '%s\n' "origin/main not found; run git fetch origin first." >&2
  exit 1
fi

printf '%-10s %-8s %-7s %-7s %-7s %s\n' "STATE" "DIRTY" "BEHIND" "AHEAD" "BRANCH" "PATH"

git -C "$repo_root" worktree list --porcelain |
awk '
  /^worktree / {
    if (path != "") print path "|" branch
    path = substr($0, 10)
    branch = "detached"
  }
  /^branch / { branch = substr($0, 19) }
  END { if (path != "") print path "|" branch }
' |
while IFS='|' read -r path branch; do
  [ -n "$path" ] || continue

  dirty=$(git -C "$path" status --porcelain 2>/dev/null | wc -l | tr -d ' ')
  counts=$(git -C "$path" rev-list --left-right --count "$main_ref...HEAD" 2>/dev/null || printf '0 0')
  behind=$(printf '%s\n' "$counts" | awk '{ print $1 }')
  ahead=$(printf '%s\n' "$counts" | awk '{ print $2 }')
  state=active

  if [ "$path" = "$repo_root" ]; then
    state=main
    [ "$dirty" -gt 0 ] && state=main-dirty
  elif [ "$dirty" -gt 0 ]; then
    state=dirty
  elif git -C "$path" merge-base --is-ancestor HEAD "$main_ref" 2>/dev/null; then
    state=merged
  fi

  printf '%-10s %-8s %-7s %-7s %-7s %s\n' "$state" "$dirty" "$behind" "$ahead" "$branch" "$path"
done
