#!/usr/bin/env bash

set -Eeuo pipefail

project_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
source_script="$project_root/scripts/deploy-production.sh"

if [[ ! -x "$source_script" ]]; then
  echo "missing executable deployment script: $source_script" >&2
  exit 1
fi

workspace="$(mktemp -d)"
trap 'rm -rf "$workspace"' EXIT

repo="$workspace/repo"
bin_dir="$workspace/bin"
mkdir -p "$repo/scripts" "$repo/nginx/conf.d" "$bin_dir"
cp "$source_script" "$repo/scripts/deploy-production.sh"
chmod +x "$repo/scripts/deploy-production.sh"
awk '/^case "\$MODE" in/{exit} {print}' "$repo/scripts/deploy-production.sh" >"$repo/scripts/deploy-functions.sh"
: >"$repo/nginx/conf.d/00-real-ip.conf"
: >"$repo/nginx/api.atoman.org.conf"
: >"$repo/docker-compose.dev.yml"

cat >"$repo/.env.prod" <<'EOF'
DATABASE_TYPE=postgres
DATABASE_URL=postgres://atoman:database-password@127.0.0.1:5432/atoman_prod?sslmode=disable
AUTH_CODE_SECRET=test-auth-code-secret
BASE_URL=https://api.example.test
S3_BUCKET=test-bucket
POSTGRES_PASSWORD=compose-password
EOF

cat >"$bin_dir/docker" <<'EOF'
#!/bin/sh
set -eu

[ "$1" = "compose" ] || exit 1
shift
if [ "$1" = "version" ]; then
  exit 0
fi

[ "$1" = "--env-file" ] || exit 1
[ "${2##*/}" = ".env.prod" ] || exit 1
[ "$3" = "-f" ] || exit 1
[ "${4##*/}" = "docker-compose.dev.yml" ] || exit 1
[ "$5" = "config" ] || exit 1
[ "$6" = "--quiet" ] || exit 1

: > "$DOCKER_CALL_FILE"
exit 0
EOF
chmod +x "$bin_dir/docker"

cat >"$bin_dir/go" <<'EOF'
#!/bin/sh
if [ "$1" = "env" ] && [ "$2" = "GOVERSION" ]; then
  printf 'go1.24.0\n'
  exit 0
fi
exit 1
EOF
chmod +x "$bin_dir/go"

source "$repo/scripts/deploy-functions.sh"
PATH="$bin_dir:$PATH"

check_prerequisites
DOCKER_CALL_FILE="$workspace/docker-compose-config-called" run_compose config --quiet
[ -f "$workspace/docker-compose-config-called" ]

grep -q '^POSTGRES_PASSWORD=' "$repo/.env.prod"

sed '/^POSTGRES_PASSWORD=/d' "$repo/.env.prod" >"$repo/.env.prod.missing-password"
if output="$(
  (
    export ENV_FILE="$repo/.env.prod.missing-password"
    check_prerequisites
  ) 2>&1
)"; then
  echo "expected a missing POSTGRES_PASSWORD to fail prerequisites" >&2
  exit 1
fi
printf '%s' "$output" | grep -q 'missing POSTGRES_PASSWORD'

echo "deployment environment checks passed"
