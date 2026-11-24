#!/usr/bin/env bash
# Ephemeral MinIO only. No production bucket, public URL, or production secret.
set -Eeuo pipefail
umask 077
if [[ "${PAPERS_P0_S3_CONFIRM:-}" != 1 ]]; then
  echo 'Explicit PAPERS_P0_S3_CONFIRM=1 required for isolated test' >&2; exit 2
fi
for tool in docker go curl od; do command -v "$tool" >/dev/null || exit 2; done
readonly root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
container=''
cleanup(){
  local code=$?
  trap - EXIT INT TERM
  if [[ -n "$container" ]]; then
    if ! docker rm -f "$container" >/dev/null 2>&1;then
      echo "Failed to remove owned P0 MinIO container ID $container" >&2
      exit 1
    fi
  fi
  echo 'P0_ISOLATED_MINIO_CONTAINER_CLEANED'
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Distinct credentials are generated for this single disposable container,
# not read from atoman-backend.service or production .env files.
suffix="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
export PAPERS_P0_S3_TEST_ACCESS_ID="papersp0$suffix"
export PAPERS_P0_S3_TEST_SECRET="P0Only-S3-Conditional-$(od -An -N18 -tx1 /dev/urandom | tr -d ' \n')"
container="$(docker run --detach --rm \
 --name "papers-p0-s3-$suffix" \
 --label atoman.test=isolated --label atoman.papers.s3=p0 \
 --security-opt no-new-privileges --cap-drop ALL \
 --tmpfs /data:rw,size=256m \
 -e MINIO_ROOT_USER="$PAPERS_P0_S3_TEST_ACCESS_ID" \
 -e MINIO_ROOT_PASSWORD="$PAPERS_P0_S3_TEST_SECRET" \
 -p 127.0.0.1::9000 \
 minio/minio:latest server /data --address :9000)"
binding="$(docker port "$container" 9000/tcp)"
if [[ ! "$binding" =~ ^127\.0\.0\.1:([0-9]+)$ ]];then
  echo 'MinIO was not bound to random loopback port' >&2;exit 1
fi
export PAPERS_P0_S3_TEST_ENDPOINT="http://127.0.0.1:${BASH_REMATCH[1]}"
ready=false
for ((i=0;i<70;i++));do
  if curl -fsS --connect-timeout 1 --max-time 2 "$PAPERS_P0_S3_TEST_ENDPOINT/minio/health/ready" >/dev/null 2>&1;then
    ready=true;break
  fi
  sleep 0.25
done
[[ "$ready" == true ]] || { echo 'Disposable MinIO not ready' >&2;exit 1; }
echo 'P0_PRIVATE_MINIO_LOOPBACK_READY'
cd "$root"
go test -race ./internal/platform/papersassets -run '^TestP0RealMinIO' -count=1 -v
echo 'P0_PRIVATE_S3_REAL_CONDITIONAL_WRITE_E2E_PASS'
