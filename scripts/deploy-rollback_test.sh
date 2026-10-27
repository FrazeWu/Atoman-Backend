#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workspace="$(mktemp -d)"
trap 'rm -rf "$workspace"' EXIT
awk '/^case "\$MODE" in/{exit} {print}' "$root/scripts/deploy-production.sh" > "$workspace/functions.sh"

for scenario in success api worker local public; do
  case_dir="$workspace/$scenario"
  mkdir -p "$case_dir/repo" "$case_dir/state"
  printf 'old-api' > "$case_dir/repo/start_server"
  printf 'old-worker' > "$case_dir/repo/music_import_worker"
  printf 'new-api' > "$case_dir/new-api"
  printf 'new-worker' > "$case_dir/new-worker"
  : > "$case_dir/repo/.env.prod"
  : > "$case_dir/calls"
  set +e
  (
    source "$workspace/functions.sh"
    REPO_DIR="$case_dir/repo"
    STATE_DIR="$case_dir/state"
    ENV_FILE="$REPO_DIR/.env.prod"
    TEMP_BINARY="$case_dir/new-api"
    TEMP_WORKER_BINARY="$case_dir/new-worker"
    LOCAL_HEALTH_URL=local
    PUBLIC_HEALTH_URL=public
    run_root() { "$@"; }
    install_systemd_unit() { :; }
    install_music_import_worker_unit() { :; }
    show_service_failure() { :; }
    systemctl() {
      printf '%s\n' "$*" >> "$case_dir/calls"
      if [[ "$1" == restart && "$(cat "$REPO_DIR/start_server")" == new-api ]]; then
        [[ "$scenario" != api || "$2" != "$SERVICE_NAME" ]] || return 1
        [[ "$scenario" != worker || "$2" != "$WORKER_SERVICE_NAME" ]] || return 1
      fi
      return 0
    }
    wait_for_url() {
      [[ "$(cat "$REPO_DIR/start_server")" == old-api ]] && return 0
      [[ "$scenario" != "$1" ]]
    }
    activate_backend
  ) > "$case_dir/output" 2>&1
  rc=$?
  set -e
  if [[ "$scenario" == success ]]; then
    [[ "$rc" == 0 ]] || { cat "$case_dir/output"; exit 1; }
    [[ "$(cat "$case_dir/repo/start_server")" == new-api ]]
    [[ "$(cat "$case_dir/repo/music_import_worker")" == new-worker ]]
  else
    [[ "$rc" != 0 ]] || { echo "failure was hidden: $scenario"; exit 1; }
    [[ "$(cat "$case_dir/repo/start_server")" == old-api ]] || { echo "API was not restored: $scenario"; exit 1; }
    [[ "$(cat "$case_dir/repo/music_import_worker")" == old-worker ]] || { echo "worker was not restored: $scenario"; exit 1; }
    grep -q 'restart atoman-backend' "$case_dir/calls"
    grep -q 'restart atoman-music-import-worker' "$case_dir/calls"
  fi
  echo "deployment transaction verified: $scenario"
done
