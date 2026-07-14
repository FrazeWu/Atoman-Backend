#!/usr/bin/env bash
set -Eeuo pipefail

MODE="${1:-update}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd -P)"

SERVICE_NAME="${ATOMAN_SERVICE_NAME:-atoman-backend}"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
ENV_FILE="${ATOMAN_ENV_FILE:-$REPO_DIR/.env.prod}"
COMPOSE_FILE="${ATOMAN_COMPOSE_FILE:-$REPO_DIR/docker-compose.dev.yml}"
CERT_DIR="${ATOMAN_CERT_DIR:-$REPO_DIR/nginx/ssl}"
NGINX_REAL_IP_SOURCE="$REPO_DIR/nginx/conf.d/00-real-ip.conf"
NGINX_SITE_SOURCE="$REPO_DIR/nginx/api.atoman.org.conf"
NGINX_REAL_IP_TARGET="/etc/nginx/conf.d/00-real-ip.conf"
NGINX_SITE_TARGET="/etc/nginx/conf.d/api.atoman.org.conf"
LOCAL_HEALTH_URL="${ATOMAN_LOCAL_HEALTH_URL:-http://127.0.0.1:8080/api/v1/site/access}"
PUBLIC_HEALTH_URL="${ATOMAN_PUBLIC_HEALTH_URL:-https://api.atoman.org/api/v1/site/access}"
LOCK_FILE="${ATOMAN_DEPLOY_LOCK_FILE:-/tmp/atoman-backend-deploy.lock}"
STATE_DIR="${ATOMAN_STATE_DIR:-/var/lib/$SERVICE_NAME}"

TEMP_BINARY=""
BACKUP_BINARY=""

log() {
  printf '[deploy] %s\n' "$*"
}

die() {
  printf '[deploy] ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: scripts/deploy-production.sh <install|update|check>

  install  First deployment: configure PostgreSQL, systemd, Nginx and start backend.
  update   Fast-forward main, rebuild, restart and roll back the binary on failure.
  check    Validate production prerequisites without changing the system.

Required before install:
  - Git, Go 1.24+, Docker Compose, systemd, Nginx and curl
  - .env.prod
  - nginx/ssl/atoman.org.pem and nginx/ssl/atoman.org.key
EOF
}

cleanup() {
  [[ -z "$TEMP_BINARY" ]] || rm -f "$TEMP_BINARY"
}
trap cleanup EXIT

run_root() {
  if [[ "$EUID" -eq 0 ]]; then
    "$@"
  else
    sudo "$@"
  fi
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "missing command: $1"
}

require_file() {
  [[ -f "$1" ]] || die "missing file: $1"
}

check_go_version() {
  local version major minor
  version="$(go env GOVERSION)"
  version="${version#go}"
  major="${version%%.*}"
  minor="${version#*.}"
  minor="${minor%%.*}"
  if (( major < 1 || (major == 1 && minor < 24) )); then
    die "Go 1.24+ is required, found $(go version)"
  fi
}

check_prerequisites() {
  local command_name required_env
  for command_name in git go docker curl flock systemctl nginx install; do
    require_command "$command_name"
  done
  if [[ "$EUID" -ne 0 ]]; then
    require_command sudo
  fi

  docker compose version >/dev/null 2>&1 || die "Docker Compose plugin is required"
  check_go_version

  require_file "$ENV_FILE"
  require_file "$COMPOSE_FILE"
  require_file "$NGINX_REAL_IP_SOURCE"
  require_file "$NGINX_SITE_SOURCE"

  for required_env in DATABASE_TYPE DATABASE_URL JWT_SECRET BASE_URL S3_BUCKET; do
    grep -q "^${required_env}=" "$ENV_FILE" || die "$ENV_FILE is missing $required_env"
  done

  if [[ "$MODE" == "install" ]]; then
    require_file "$CERT_DIR/atoman.org.pem"
    require_file "$CERT_DIR/atoman.org.key"
  fi
}

require_clean_main() {
  local branch
  branch="$(git -C "$REPO_DIR" branch --show-current)"
  [[ "$branch" == "main" ]] || die "deployment requires main branch, found: ${branch:-detached HEAD}"
  [[ -z "$(git -C "$REPO_DIR" status --porcelain --untracked-files=normal)" ]] \
    || die "backend worktree has uncommitted files"
}

sync_source() {
  require_clean_main
  log "Fetching origin/main"
  git -C "$REPO_DIR" fetch origin main
  git -C "$REPO_DIR" merge --ff-only origin/main
}

wait_for_postgres() {
  local container_id state attempt
  container_id="$(docker compose -f "$COMPOSE_FILE" ps -q postgres)"
  [[ -n "$container_id" ]] || die "PostgreSQL container was not created"

  for attempt in {1..30}; do
    state="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container_id")"
    if [[ "$state" == "healthy" || "$state" == "running" ]]; then
      log "PostgreSQL is $state"
      return 0
    fi
    sleep 2
  done
  die "PostgreSQL did not become healthy"
}

start_postgres() {
  log "Starting local PostgreSQL"
  docker compose -f "$COMPOSE_FILE" up -d postgres db-init
  wait_for_postgres
}

build_backend() {
  TEMP_BINARY="$(mktemp "$REPO_DIR/.start_server.new.XXXXXX")"
  log "Building backend"
  (
    cd "$REPO_DIR"
    go build ./...
    go build -trimpath -o "$TEMP_BINARY" ./cmd/start_server
  )
  chmod 0755 "$TEMP_BINARY"
}

install_systemd_unit() {
  local default_service_user service_user service_group unit_tmp
  default_service_user="${SUDO_USER:-$(id -un)}"
  service_user="${ATOMAN_SERVICE_USER:-$default_service_user}"
  service_group="${ATOMAN_SERVICE_GROUP:-$(id -gn "$service_user")}"
  unit_tmp="$(mktemp)"

  cat >"$unit_tmp" <<EOF
[Unit]
Description=Atoman Backend
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
Type=simple
User=$service_user
Group=$service_group
WorkingDirectory=$REPO_DIR
EnvironmentFile=$ENV_FILE
Environment=ENV=production
Environment=GIN_MODE=release
Environment=PORT=8080
ExecStart=$REPO_DIR/start_server --mode prod
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

  run_root install -m 0644 "$unit_tmp" "$SERVICE_FILE"
  rm -f "$unit_tmp"
  run_root systemctl daemon-reload
  run_root systemctl enable "$SERVICE_NAME" >/dev/null
}

install_nginx_config() {
  local nginx_backup had_real_ip=false had_site=false
  nginx_backup="$(mktemp -d)"

  run_root mkdir -p /etc/nginx/conf.d /etc/nginx/ssl
  if [[ -f "$NGINX_REAL_IP_TARGET" ]]; then
    had_real_ip=true
    run_root cp "$NGINX_REAL_IP_TARGET" "$nginx_backup/00-real-ip.conf"
  fi
  if [[ -f "$NGINX_SITE_TARGET" ]]; then
    had_site=true
    run_root cp "$NGINX_SITE_TARGET" "$nginx_backup/api.atoman.org.conf"
  fi

  if [[ "$MODE" == "install" ]]; then
    run_root install -m 0644 "$CERT_DIR/atoman.org.pem" /etc/nginx/ssl/api.atoman.org.pem
    run_root install -m 0600 "$CERT_DIR/atoman.org.key" /etc/nginx/ssl/api.atoman.org.key
  fi

  run_root install -m 0644 "$NGINX_REAL_IP_SOURCE" "$NGINX_REAL_IP_TARGET"
  run_root install -m 0644 "$NGINX_SITE_SOURCE" "$NGINX_SITE_TARGET"

  if ! run_root nginx -t; then
    if [[ "$had_real_ip" == true ]]; then
      run_root cp "$nginx_backup/00-real-ip.conf" "$NGINX_REAL_IP_TARGET"
    else
      run_root rm -f "$NGINX_REAL_IP_TARGET"
    fi
    if [[ "$had_site" == true ]]; then
      run_root cp "$nginx_backup/api.atoman.org.conf" "$NGINX_SITE_TARGET"
    else
      run_root rm -f "$NGINX_SITE_TARGET"
    fi
    rm -rf "$nginx_backup"
    die "Nginx configuration check failed; previous configuration restored"
  fi

  run_root systemctl enable nginx >/dev/null
  run_root systemctl reload-or-restart nginx
  rm -rf "$nginx_backup"
}

wait_for_url() {
  local url="$1" attempt
  for attempt in {1..30}; do
    if curl --fail --silent --show-error --max-time 5 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
}

show_service_failure() {
  run_root systemctl status "$SERVICE_NAME" --no-pager -l || true
  run_root journalctl -u "$SERVICE_NAME" -n 80 --no-pager || true
}

rollback_binary() {
  [[ -n "$BACKUP_BINARY" && -f "$BACKUP_BINARY" ]] || return 1
  log "Restoring previous backend binary"
  run_root install -m 0755 "$BACKUP_BINARY" "$REPO_DIR/start_server"
  run_root systemctl restart "$SERVICE_NAME"
  wait_for_url "$LOCAL_HEALTH_URL"
}

activate_backend() {
  run_root mkdir -p "$STATE_DIR"
  if [[ -f "$REPO_DIR/start_server" ]]; then
    BACKUP_BINARY="$STATE_DIR/start_server.previous"
    run_root install -m 0755 "$REPO_DIR/start_server" "$BACKUP_BINARY"
  fi

  run_root install -m 0755 "$TEMP_BINARY" "$REPO_DIR/start_server"
  run_root chmod 0600 "$ENV_FILE"
  install_systemd_unit

  log "Restarting $SERVICE_NAME"
  if ! run_root systemctl restart "$SERVICE_NAME"; then
    show_service_failure
    rollback_binary || true
    die "service restart failed"
  fi

  if ! wait_for_url "$LOCAL_HEALTH_URL"; then
    show_service_failure
    rollback_binary || true
    die "local health check failed"
  fi

  if ! wait_for_url "$PUBLIC_HEALTH_URL"; then
    show_service_failure
    rollback_binary || true
    die "public health check failed"
  fi
}

check_runtime() {
  check_prerequisites
  log "Repository: $REPO_DIR"
  log "Environment: $ENV_FILE"
  log "Compose: $COMPOSE_FILE"

  if systemctl is-active --quiet docker; then
    log "Docker is active"
  else
    die "Docker is not active"
  fi

  if [[ -f "$SERVICE_FILE" ]]; then
    systemctl is-active --quiet "$SERVICE_NAME" && log "$SERVICE_NAME is active" || log "$SERVICE_NAME is not active"
  else
    log "$SERVICE_NAME is not installed"
  fi

  if run_root nginx -t >/dev/null 2>&1; then
    log "Nginx configuration is valid"
  else
    die "Nginx configuration is invalid"
  fi
}

case "$MODE" in
  -h|--help|help)
    usage
    exit 0
    ;;
  install|update|check)
    ;;
  *)
    usage >&2
    die "unknown mode: $MODE"
    ;;
esac

exec 9>"$LOCK_FILE"
flock -n 9 || die "another deployment is running"

if [[ "$MODE" == "check" ]]; then
  check_runtime
  exit 0
fi

check_prerequisites
sync_source
start_postgres
build_backend
install_nginx_config
activate_backend

log "Deployment completed"
log "Commit: $(git -C "$REPO_DIR" rev-parse --short HEAD)"
