#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workspace="$(mktemp -d)"
trap 'rm -rf "$workspace"' EXIT
mkdir -p "$workspace/bin"
export AUDIT_TEST_LOG="$workspace/log"
cat > "$workspace/bin/docker" <<'MOCK'
#!/usr/bin/env bash
set -eu
printf 'docker %s\n' "$*" >> "$AUDIT_TEST_LOG"
case "$1" in
  run) [[ "${MOCK_RUN_FAIL:-0}" == 0 ]] || exit 7; echo audit-owned-container ;;
  exec) exit 0 ;;
  port) echo "${MOCK_BINDING:-127.0.0.1:45678}" ;;
  rm) [[ "$*" == 'rm -f audit-owned-container' ]] || exit 91 ;;
  *) exit 92 ;;
esac
MOCK
cat > "$workspace/bin/go" <<'MOCK'
#!/usr/bin/env bash
set -eu
printf 'go %s\n' "$*" >> "$AUDIT_TEST_LOG"
[[ "$TEST_POSTGRES_DSN" == "$EXPECTED_TEST_DSN" ]]
for key in TEST_DATABASE_URL REFERENCE_TEST_POSTGRES_DSN FORUM_TEST_POSTGRES_DSN MUSIC_POSTGRES_TEST_DSN; do
  [[ "${!key}" == "$TEST_POSTGRES_DSN" ]]
done
exit "${MOCK_GO_EXIT:-0}"
MOCK
chmod +x "$workspace/bin/docker" "$workspace/bin/go"
export PATH="$workspace/bin:$PATH"
unset TEST_POSTGRES_DSN TEST_DATABASE_URL
export EXPECTED_TEST_DSN='postgres://atoman_test:atoman_test@127.0.0.1:45678/atoman_test?sslmode=disable'
: > "$AUDIT_TEST_LOG"
"$ROOT_DIR/scripts/test-isolated.sh" ./internal/testdb -count=1
grep -q 'go test -p 1 ./internal/testdb -count=1' "$AUDIT_TEST_LOG"
grep -q 'docker rm -f audit-owned-container' "$AUDIT_TEST_LOG"
grep -q -- '-p 127.0.0.1::5432' "$AUDIT_TEST_LOG"

: > "$AUDIT_TEST_LOG"
set +e
MOCK_GO_EXIT=17 "$ROOT_DIR/scripts/test-isolated.sh" >/dev/null
rc=$?
set -e
[[ "$rc" == 17 ]]
grep -q 'go test -p 1 ./...' "$AUDIT_TEST_LOG"
grep -q 'docker rm -f audit-owned-container' "$AUDIT_TEST_LOG"

: > "$AUDIT_TEST_LOG"
TEST_POSTGRES_DSN=postgres://explicit.example/test EXPECTED_TEST_DSN=postgres://explicit.example/test \
  "$ROOT_DIR/scripts/test-isolated.sh" ./internal/testdb >/dev/null
! grep -q '^docker ' "$AUDIT_TEST_LOG"
TEST_DATABASE_URL=postgres://legacy.example/test EXPECTED_TEST_DSN=postgres://legacy.example/test \
  "$ROOT_DIR/scripts/test-isolated.sh" ./internal/testdb >/dev/null
! grep -q '^docker ' "$AUDIT_TEST_LOG"

: > "$AUDIT_TEST_LOG"
if MOCK_RUN_FAIL=1 "$ROOT_DIR/scripts/test-isolated.sh" >/dev/null 2>&1; then exit 93; fi
! grep -q '^docker rm' "$AUDIT_TEST_LOG"
! grep -q '^go ' "$AUDIT_TEST_LOG"

: > "$AUDIT_TEST_LOG"
if MOCK_BINDING=0.0.0.0:45678 "$ROOT_DIR/scripts/test-isolated.sh" >/dev/null 2>&1; then exit 94; fi
grep -q 'docker rm -f audit-owned-container' "$AUDIT_TEST_LOG"
! grep -q '^go ' "$AUDIT_TEST_LOG"

: > "$AUDIT_TEST_LOG"
if GO_TEST_PARALLELISM=0 "$ROOT_DIR/scripts/test-isolated.sh" >/dev/null 2>&1; then exit 95; fi
[[ ! -s "$AUDIT_TEST_LOG" ]]
echo 'isolated test database lifecycle checks passed'
