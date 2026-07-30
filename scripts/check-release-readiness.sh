#!/bin/sh

set -eu

role="${1:-}"
repo_root="${ATOMAN_REPO_ROOT:-$(git rev-parse --show-toplevel)}"
workspace_root="${ATOMAN_WORKSPACE_ROOT:-$(dirname "$repo_root")}"
frontend_root="$workspace_root/Atoman-Frontend"
backend_root="$workspace_root/Atoman-Backend"
notes_path="docs/release/release-notes-draft.md"

fail() {
  echo "release check failed: $*" >&2
  exit 1
}

read_frontend_version() {
  bun -e 'const input = await Bun.stdin.text(); process.stdout.write(String(JSON.parse(input).version ?? ""))'
}

section_has_content() {
  heading="$1"
  awk -v heading="$heading" '
    $0 == heading { active = 1; next }
    active && /^## / { active = 0 }
    active && $0 !~ /^#/ && $0 ~ /[^[:space:]]/ { found = 1 }
    END { exit found ? 0 : 1 }
  '
}

check_bilingual_notes() {
  label="$1"
  content="$2"

  printf '%s\n' "$content" | section_has_content '## 中文' || fail "$label Chinese section has no content"
  printf '%s\n' "$content" | section_has_content '## English' || fail "$label English section has no content"
}

case "$role" in
  frontend)
    version_path="package.json"
    if git -C "$repo_root" diff --cached --quiet -- "$version_path"; then
      exit 0
    fi
    command -v bun >/dev/null 2>&1 || fail "bun is required to read package.json"
    version="$(git -C "$repo_root" show ":$version_path" | read_frontend_version)"
    [ -f "$backend_root/VERSION" ] || fail "missing backend VERSION at $backend_root/VERSION"
    counterpart_version="$(tr -d '[:space:]' <"$backend_root/VERSION")"
    ;;
  backend)
    version_path="VERSION"
    if git -C "$repo_root" diff --cached --quiet -- "$version_path"; then
      exit 0
    fi
    version="$(git -C "$repo_root" show ":$version_path" | tr -d '[:space:]')"
    [ -f "$frontend_root/package.json" ] || fail "missing frontend package.json at $frontend_root/package.json"
    command -v bun >/dev/null 2>&1 || fail "bun is required to read package.json"
    counterpart_version="$(read_frontend_version <"$frontend_root/package.json")"
    ;;
  *)
    fail "role must be frontend or backend"
    ;;
esac

printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || fail "invalid version: $version"
[ "$version" = "$counterpart_version" ] || fail "frontend/backend version mismatch: $version != $counterpart_version"

if git -C "$repo_root" diff --cached --quiet -- "$notes_path"; then
  fail "stage release notes at $notes_path with the version change"
fi

staged_notes="$(git -C "$repo_root" show ":$notes_path")"
check_bilingual_notes "$role" "$staged_notes"

[ -f "$frontend_root/$notes_path" ] || fail "missing frontend release notes"
[ -f "$backend_root/$notes_path" ] || fail "missing backend release notes"
check_bilingual_notes "frontend" "$(cat "$frontend_root/$notes_path")"
check_bilingual_notes "backend" "$(cat "$backend_root/$notes_path")"

echo "release check passed for $role v$version"
