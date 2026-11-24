#!/usr/bin/env bash
# Disposable, non-admin IAM security acceptance against pinned MinIO.
# Does not contact production MinIO, create persistent users or change buckets.
set -Eeuo pipefail
umask 077
if [[ "${PAPERS_P0_IAM_CONFIRM:-}" != 1 || "${PAPERS_P0_S3_CONFIRM:-}" != 1 ]];then
 echo 'Both disposable IAM and S3 test confirmations required' >&2;exit 2
fi
for tool in docker aws curl od go;do command -v "$tool" >/dev/null || exit 2;done
readonly code_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly image="minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
docker image inspect "$image" >/dev/null || { echo 'Expected pinned MinIO image missing' >&2;exit 2; }
testroot="$(mktemp -d /var/tmp/atoman-papers-p0-iam.XXXXXXXX)"
chmod 0700 "$testroot"
container=''
cleanup(){
 local rc=$?
 trap - EXIT INT TERM
 local failed=0
 if [[ -n "$container" ]];then
  docker rm -f "$container" >/dev/null 2>&1 || failed=1
 fi
 case "$testroot" in /var/tmp/atoman-papers-p0-iam.*)
  rm -rf -- "$testroot" || failed=1 ;;
  *) failed=1 ;;
 esac
 if (( failed!=0 ));then echo 'Disposable IAM cleanup failed' >&2;exit 1;fi
 echo 'P0_DISPOSABLE_IAM_MINIO_REMOVED'
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
suffix="$(od -An -N5 -tx1 /dev/urandom | tr -d ' \n')"
randpass(){ printf 'P0Only_%s' "$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"; }
export PAPERS_P0_IAM_ADMIN_USER="papersp0root$suffix"
export PAPERS_P0_IAM_ADMIN_SECRET="$(randpass)"
export PAPERS_P0_IAM_PUBLISHER_USER="papersp0pub$suffix"
export PAPERS_P0_IAM_PUBLISHER_SECRET="$(randpass)"
export PAPERS_P0_IAM_READER_USER="papersp0read$suffix"
export PAPERS_P0_IAM_READER_SECRET="$(randpass)"
export PAPERS_P0_IAM_BUCKET="papers-private-p0-$suffix"
container="$(docker run --detach --rm --name "papers-p0-iam-$suffix" \
 --label atoman.test=isolated --label atoman.papers.iam=p0 \
 --security-opt no-new-privileges --cap-drop ALL \
 --tmpfs /data:rw,size=256m \
 -e MINIO_ROOT_USER="$PAPERS_P0_IAM_ADMIN_USER" \
 -e MINIO_ROOT_PASSWORD="$PAPERS_P0_IAM_ADMIN_SECRET" \
 -p 127.0.0.1::9000 "$image" server /data --address :9000)"
binding="$(docker port "$container" 9000/tcp)"
if [[ ! "$binding" =~ ^127\.0\.0\.1:([0-9]+)$ ]];then
 echo 'MinIO must bind only random loopback port' >&2;exit 1
fi
export PAPERS_P0_IAM_ENDPOINT="http://127.0.0.1:${BASH_REMATCH[1]}"
export PAPERS_P0_S3_TEST_ENDPOINT="$PAPERS_P0_IAM_ENDPOINT"
export PAPERS_P0_S3_TEST_ACCESS_ID="$PAPERS_P0_IAM_ADMIN_USER"
export PAPERS_P0_S3_TEST_SECRET="$PAPERS_P0_IAM_ADMIN_SECRET"
export AWS_ACCESS_KEY_ID="$PAPERS_P0_IAM_ADMIN_USER"
export AWS_SECRET_ACCESS_KEY="$PAPERS_P0_IAM_ADMIN_SECRET"
export AWS_DEFAULT_REGION=us-east-1
export AWS_EC2_METADATA_DISABLED=true
ready=false
for ((i=0;i<90;i++));do
 if curl -fsS --connect-timeout 1 --max-time 2 "$PAPERS_P0_IAM_ENDPOINT/minio/health/ready" >/dev/null 2>&1;then
  ready=true;break
 fi
 sleep 0.25
done
[[ "$ready" == true ]] || { echo 'IAM MinIO not ready' >&2;exit 1; }
echo 'P0_IAM_LOOPBACK_MINIO_READY'
aws --endpoint-url "$PAPERS_P0_IAM_ENDPOINT" s3api create-bucket \
 --bucket "$PAPERS_P0_IAM_BUCKET" --object-lock-enabled-for-bucket > "$testroot/create.json"
aws --endpoint-url "$PAPERS_P0_IAM_ENDPOINT" s3api put-object-lock-configuration \
 --bucket "$PAPERS_P0_IAM_BUCKET" \
 --object-lock-configuration '{"ObjectLockEnabled":"Enabled","Rule":{"DefaultRetention":{"Mode":"COMPLIANCE","Days":1}}}' \
 > "$testroot/lock.log"
admin_bin="${PAPERS_P0_IAM_ADMIN_BINARY:-}"
if [[ "$admin_bin" == "" ]];then
 (cd "$code_root/scripts/papers/p0_minio_iam" && go build -o "$testroot/iam-admin" .)
 admin_bin="$testroot/iam-admin"
fi
"$admin_bin"
echo 'P0_IAM_TWO_SCOPED_NON_ADMIN_CREDENTIALS_CREATED'
cd "$code_root"
if [[ "${PAPERS_P0_IAM_UNSAFE_TEST_MODE:-}" == 1 ]];then
 go test -race ./internal/platform/papersassets -run '^TestP0IAMNonAdminMinimalRoles$' -count=1 -v
 echo 'P0_NONADMIN_ROLE_ISOLATION_PASS_BUT_CONDITIONAL_POLICY_UNSUPPORTED'
else
 go test -race ./internal/platform/papersassets -run '^TestP0IAMNonAdminWriterReaderAndWriteOnce$' -count=1 -v
 echo 'P0_IAM_NONADMIN_POLICY_NEGATIVE_TESTS_PASS'
fi
