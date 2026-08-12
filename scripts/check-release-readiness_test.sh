#!/bin/sh

set -eu

project_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
check_script="$project_root/scripts/check-release-readiness.sh"

if [ ! -x "$check_script" ]; then
  echo "missing executable release check: $check_script" >&2
  exit 1
fi

workspace="$(mktemp -d)"
trap 'rm -rf "$workspace"' EXIT

frontend="$workspace/Atoman-Frontend"
backend="$workspace/Atoman-Backend"
mkdir -p "$frontend/docs/release" "$backend/docs/release"

init_repo() {
  git -C "$1" init -q
  git -C "$1" config user.name "Release Check Test"
  git -C "$1" config user.email "release-check@example.com"
}

write_notes() {
  notes_path="$1"
  english_body="$2"
  cat >"$notes_path" <<EOF
# Release Notes Draft

## 中文

当前版本发布说明。

## English

$english_body
EOF
}

init_repo "$frontend"
init_repo "$backend"

cat >"$frontend/package.json" <<'EOF'
{
  "name": "atoman",
  "version": "0.1.0"
}
EOF
write_notes "$frontend/docs/release/release-notes-draft.md" "Current release notes."
printf '%s\n' '0.1.0' >"$backend/VERSION"
write_notes "$backend/docs/release/release-notes-draft.md" "Current release notes."

git -C "$frontend" add .
git -C "$frontend" commit -qm baseline
git -C "$backend" add .
git -C "$backend" commit -qm baseline

run_check() {
  role="$1"
  repo="$2"
  ATOMAN_REPO_ROOT="$repo" ATOMAN_WORKSPACE_ROOT="$workspace" "$check_script" "$role"
}

run_check frontend "$frontend"
run_check backend "$backend"

sed -i 's/"version": "0.1.0"/"version": "0.2.0"/' "$frontend/package.json"
git -C "$frontend" add package.json

if output="$(run_check frontend "$frontend" 2>&1)"; then
  echo "expected a frontend/backend version mismatch" >&2
  exit 1
fi
printf '%s' "$output" | grep -q 'version mismatch'

printf '%s\n' '0.2.0' >"$backend/VERSION"
if output="$(run_check frontend "$frontend" 2>&1)"; then
  echo "expected staged release notes to be required" >&2
  exit 1
fi
printf '%s' "$output" | grep -q 'stage release notes'

write_notes "$frontend/docs/release/release-notes-draft.md" ""
git -C "$frontend" add docs/release/release-notes-draft.md
if output="$(run_check frontend "$frontend" 2>&1)"; then
  echo "expected missing English release notes to fail" >&2
  exit 1
fi
printf '%s' "$output" | grep -q 'English section has no content'

write_notes "$frontend/docs/release/release-notes-draft.md" "Frontend release notes."
git -C "$frontend" add docs/release/release-notes-draft.md
run_check frontend "$frontend"

write_notes "$backend/docs/release/release-notes-draft.md" "Backend release notes."
git -C "$backend" add VERSION docs/release/release-notes-draft.md
run_check backend "$backend"

echo "release readiness checks passed"
