#!/usr/bin/env bash
# Disposable P0 MinIO publisher-gateway security test. No production credentials.
set -Eeuo pipefail
umask 077
if [[ "$(printenv PAPERS_P0_GATEWAY_CONFIRM || true)" != "1" || "$(id -u)" != 0 ]];then
 echo "Explicit root P0 disposable gateway confirmation required" >&2; exit 2
fi
for x in go docker aws curl sha256sum setpriv od cmp;do command -v "$x" >/dev/null || exit 2;done
source_root="$(cd "$(dirname "$0")/../.." && pwd)"
readonly image="minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
docker image inspect "$image" >/dev/null
root="$(mktemp -d /var/tmp/atoman-papers-p0-gate.XXXXXXXX)"
chmod 0711 "$root"
container=''
gateway_pid=''
cleanup() {
 local rc=$?
 trap - EXIT INT TERM
 local bad=0
 if [[ -n "$gateway_pid" ]] && kill -0 "$gateway_pid" 2>/dev/null;then
  kill -TERM "$gateway_pid" 2>/dev/null || true
  for ((i=0;i<60;i++));do
   if ! kill -0 "$gateway_pid" 2>/dev/null;then break;fi
   sleep 0.1
  done
  if kill -0 "$gateway_pid" 2>/dev/null;then kill -KILL "$gateway_pid" 2>/dev/null || bad=1;fi
  wait "$gateway_pid" 2>/dev/null || true
 fi
 if [[ -n "$container" ]];then docker rm -f "$container" >/dev/null 2>&1 || bad=1;fi
 if (( rc!=0 ));then
  echo "=== P0 GATEWAY FAILURE LOG ===" >&2
  tail -n 70 "$root/gateway.log" 2>/dev/null >&2 || true
  tail -n 50 "$root/iam.log" 2>/dev/null >&2 || true
 fi
 case "$root" in /var/tmp/atoman-papers-p0-gate.*) rm -rf -- "$root" || bad=1;; *)bad=1;;esac
 (( bad==0 )) || { echo 'P0 gateway cleanup failed' >&2;exit 1; }
 echo 'P0_GATEWAY_EPHEMERAL_RESOURCES_REMOVED'
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir "$root/sockets" "$root/staging" "$root/client" "$root/bin"
chown 65532:65534 "$root/sockets" "$root/staging"
chmod 0750 "$root/sockets"
chmod 0700 "$root/staging"
chown 65534:65534 "$root/client"
chmod 0700 "$root/client"
chmod 0755 "$root/bin"

(cd "$source_root" && go build -o "$root/bin/p0-pubgate" ./cmd/papers_p0_pubgate)
(cd "$source_root" && go build -o "$root/bin/p0-client" ./cmd/papers_p0_pubgate_client)
(cd "$source_root/scripts/papers/p0_minio_iam" && go build -o "$root/bin/iam-provision" .)
chmod 0755 "$root/bin/p0-pubgate" "$root/bin/p0-client"

suffix="$(od -An -N5 -tx1 /dev/urandom | tr -d ' \n')"
randpass(){ printf 'P0Only_%s' "$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"; }
export PAPERS_P0_IAM_CONFIRM=1 PAPERS_P0_S3_CONFIRM=1 PAPERS_P0_IAM_GATEWAY_MODE=1
export PAPERS_P0_IAM_ADMIN_USER="papersp0root$suffix"
export PAPERS_P0_IAM_ADMIN_SECRET="$(randpass)"
export PAPERS_P0_IAM_PUBLISHER_USER="papersp0pub$suffix"
export PAPERS_P0_IAM_PUBLISHER_SECRET="$(randpass)"
export PAPERS_P0_IAM_READER_USER="papersp0read$suffix"
export PAPERS_P0_IAM_READER_SECRET="$(randpass)"
export PAPERS_P0_GATEWAY_WRITER_USER="papersp0gate$suffix"
export PAPERS_P0_GATEWAY_WRITER_SECRET="$(randpass)"
export PAPERS_P0_IAM_BUCKET="papers-private-p0-$suffix"

container="$(docker run --detach --rm --name "papers-p0-gate-$suffix" \
 --label atoman.test=isolated --label atoman.papers.gateway=p0 \
 --security-opt no-new-privileges --cap-drop ALL \
 --tmpfs /data:rw,size=256m \
 -e MINIO_ROOT_USER="$PAPERS_P0_IAM_ADMIN_USER" \
 -e MINIO_ROOT_PASSWORD="$PAPERS_P0_IAM_ADMIN_SECRET" \
 -p 127.0.0.1::9000 "$image" server /data --address :9000)"
binding="$(docker port "$container" 9000/tcp)"
[[ "$binding" =~ ^127\.0\.0\.1:[0-9]+$ ]] || { echo "Unexpected public port" >&2;exit 1; }
port="$(printf '%s' "$binding" | cut -d: -f2)"
export PAPERS_P0_IAM_ENDPOINT="http://127.0.0.1:$port"
export PAPERS_P0_GATEWAY_ENDPOINT="$PAPERS_P0_IAM_ENDPOINT"
export PAPERS_P0_GATEWAY_BUCKET="$PAPERS_P0_IAM_BUCKET"
export AWS_ACCESS_KEY_ID="$PAPERS_P0_IAM_ADMIN_USER"
export AWS_SECRET_ACCESS_KEY="$PAPERS_P0_IAM_ADMIN_SECRET"
export AWS_DEFAULT_REGION=us-east-1 AWS_EC2_METADATA_DISABLED=true
ready=false
for ((i=0;i<90;i++));do
 if curl -fsS --connect-timeout 1 --max-time 2 "$PAPERS_P0_IAM_ENDPOINT/minio/health/ready" >/dev/null 2>&1;then ready=true;break;fi
 sleep 0.2
done
[[ "$ready" == true ]] || { echo 'MinIO test server not ready' >&2;exit 1; }
aws --endpoint-url "$PAPERS_P0_IAM_ENDPOINT" s3api create-bucket \
 --bucket "$PAPERS_P0_IAM_BUCKET" --object-lock-enabled-for-bucket >/dev/null
aws --endpoint-url "$PAPERS_P0_IAM_ENDPOINT" s3api put-object-lock-configuration \
 --bucket "$PAPERS_P0_IAM_BUCKET" \
 --object-lock-configuration '{"ObjectLockEnabled":"Enabled","Rule":{"DefaultRetention":{"Mode":"COMPLIANCE","Days":1}}}' >/dev/null
"$root/bin/iam-provision" >"$root/iam.log" 2>&1
grep -q 'P0_GATEWAY_PUBLISHER_S3_PUT_REVOKED' "$root/iam.log"
grep -q 'P0_GATEWAY_THREE_SCOPED_IDENTITIES_CREATED' "$root/iam.log"
echo 'P0_GATEWAY_IAM_SEPARATE_READONLY_PUBLISHER_AND_WRITER_CREATED'

env -u PAPERS_P0_IAM_ADMIN_SECRET -u PAPERS_P0_IAM_PUBLISHER_SECRET \
 -u PAPERS_P0_IAM_READER_SECRET -u AWS_SECRET_ACCESS_KEY -u AWS_ACCESS_KEY_ID \
 -u PAPERS_P0_IAM_ADMIN_USER -u PAPERS_P0_IAM_READER_USER \
 setpriv --reuid=65532 --regid=65534 --clear-groups \
 "$root/bin/p0-pubgate" "$root" > "$root/gateway.log" 2>&1 &
gateway_pid=$!
socket="$root/sockets/publish.sock"
ready=false
for ((i=0;i<150;i++));do
 if [[ -S "$socket" ]];then ready=true;break;fi
 if ! kill -0 "$gateway_pid" 2>/dev/null;then break;fi
 sleep 0.1
done
[[ "$ready" == true ]] || { echo 'Gateway did not start' >&2;exit 1; }
[[ "$(stat -c '%a %u %g' "$socket")" == '660 65532 65534' ]] || { echo 'Gateway socket permissions wrong' >&2;exit 1; }
grep -q 'P0_GATEWAY_INDEPENDENT_PRIVATE_VERSIONING_CONDITIONAL_COMPLIANCE_PROBE_PASS' "$root/gateway.log" || { echo 'Gateway startup safety probe missing' >&2;exit 1; }
echo 'P0_GATEWAY_SEPARATE_PROCESS_UNIX_SOCKET_PEERCRED_READY'

env -u PAPERS_P0_IAM_ADMIN_SECRET -u PAPERS_P0_GATEWAY_WRITER_SECRET \
 -u PAPERS_P0_IAM_READER_SECRET -u AWS_SECRET_ACCESS_KEY -u AWS_ACCESS_KEY_ID \
 -u PAPERS_P0_IAM_ADMIN_USER -u PAPERS_P0_GATEWAY_WRITER_USER \
 setpriv --reuid=65534 --regid=65534 --clear-groups \
 "$root/bin/p0-client" "$root"

printf '%s' '{"test":"private-p0-gateway-immutable"}' > "$root/client/source.json"
chown 65534:65534 "$root/client/source.json"
chmod 0640 "$root/client/source.json"
sha="$(sha256sum "$root/client/source.json" | awk '{print $1}')"
key="papers/immutable/v1/snapshots/$sha.json"
client_curl(){
 setpriv --reuid=65534 --regid=65534 --clear-groups \
 curl -sS --max-time 30 --unix-socket "$socket" -o "$root/client/response" \
  -w '%{http_code}' -X PUT "$@" "http://papers-gateway/v1/objects/$key"
}
code="$(client_curl -H 'If-None-Match: *' --data-binary "@$root/client/source.json")"
[[ "$code" == 201 ]] || { echo "Gate first PUT: $code" >&2;exit 1; }
code="$(client_curl -H 'If-None-Match: *' --data-binary "@$root/client/source.json")"
[[ "$code" == 412 ]] || { echo "Gate duplicate not 412: $code" >&2;exit 1; }
code="$(client_curl --data-binary "@$root/client/source.json")"
[[ "$code" == 428 ]] || { echo "Gate blind PUT not denied: $code" >&2;exit 1; }
code="$(setpriv --reuid=65533 --regid=65534 --clear-groups \
 curl -sS --max-time 30 --unix-socket "$socket" -o /dev/null -w '%{http_code}' \
 -X PUT -H 'If-None-Match: *' --data 'forbidden' "http://papers-gateway/v1/objects/$key")"
[[ "$code" == 403 ]] || { echo "Wrong peer UID not denied: $code" >&2;exit 1; }

publisher_s3(){
 env AWS_ACCESS_KEY_ID="$PAPERS_P0_IAM_PUBLISHER_USER" \
 AWS_SECRET_ACCESS_KEY="$PAPERS_P0_IAM_PUBLISHER_SECRET" \
 AWS_DEFAULT_REGION=us-east-1 AWS_EC2_METADATA_DISABLED=true \
 HOME="$root/client" \
 setpriv --reuid=65534 --regid=65534 --clear-groups \
 aws --endpoint-url "$PAPERS_P0_IAM_ENDPOINT" s3api "$@"
}
publisher_s3 get-object --bucket "$PAPERS_P0_IAM_BUCKET" \
 --key "$key" "$root/client/read.json" >/dev/null
cmp "$root/client/source.json" "$root/client/read.json"
if publisher_s3 put-object --bucket "$PAPERS_P0_IAM_BUCKET" \
 --key "$key" --body "$root/client/source.json" > "$root/client/unsafe-put.log" 2>&1;then
 echo 'Critical: publisher can bypass gateway and overwrite' >&2;exit 1
fi
if publisher_s3 delete-object --bucket "$PAPERS_P0_IAM_BUCKET" \
 --key "$key" > "$root/client/unsafe-delete.log" 2>&1;then
 echo 'Critical: publisher can create delete marker' >&2;exit 1
fi
if publisher_s3 copy-object --bucket "$PAPERS_P0_IAM_BUCKET" \
 --key "$key" --copy-source "$PAPERS_P0_IAM_BUCKET/$key" > "$root/client/unsafe-copy.log" 2>&1;then
 echo 'Critical: publisher can overwrite via CopyObject' >&2;exit 1
fi
version="$(aws --endpoint-url "$PAPERS_P0_IAM_ENDPOINT" s3api list-object-versions \
 --bucket "$PAPERS_P0_IAM_BUCKET" --prefix "$key" \
 --query 'Versions[0].VersionId' --output text)"
[[ -n "$version" && "$version" != None ]] || exit 1
if publisher_s3 delete-object --bucket "$PAPERS_P0_IAM_BUCKET" \
 --key "$key" --version-id "$version" > "$root/client/unsafe-delete-version.log" 2>&1;then
 echo 'Critical: publisher can delete protected version' >&2;exit 1
fi
total="$(aws --endpoint-url "$PAPERS_P0_IAM_ENDPOINT" s3api list-object-versions \
 --bucket "$PAPERS_P0_IAM_BUCKET" --prefix "$key" --query 'length(Versions)' --output text)"
[[ "$total" == 1 ]] || { echo "Unexpected object versions: $total" >&2;exit 1; }
publisher_s3 get-object --bucket "$PAPERS_P0_IAM_BUCKET" --key "$key" "$root/client/after.json" >/dev/null
cmp "$root/client/source.json" "$root/client/after.json"
echo 'P0_GATEWAY_REAL_MINIO_CONDITIONAL_PUT_REPLAY_412_PASS'
echo 'P0_GATEWAY_PUBLISHER_DIRECT_PUT_COPY_DELETE_VERSION_DENIED'
echo 'P0_GATEWAY_PEERCRED_UNAUTHORIZED_UID_DENIED'
gateway_writer_s3(){
 env AWS_ACCESS_KEY_ID="$PAPERS_P0_GATEWAY_WRITER_USER" \
 AWS_SECRET_ACCESS_KEY="$PAPERS_P0_GATEWAY_WRITER_SECRET" \
 AWS_DEFAULT_REGION=us-east-1 AWS_EC2_METADATA_DISABLED=true \
 aws --endpoint-url "$PAPERS_P0_IAM_ENDPOINT" s3api "$@"
}
if gateway_writer_s3 put-object --bucket "$PAPERS_P0_IAM_BUCKET" \
 --key 'outside-immutable/bypass.json' --body "$root/client/source.json" >/dev/null 2>&1;then
 echo 'Critical: gateway writer can write outside private prefix' >&2;exit 1
fi
if gateway_writer_s3 delete-object --bucket "$PAPERS_P0_IAM_BUCKET" \
 --key "$key" >/dev/null 2>&1;then
 echo 'Critical: gateway writer can create delete marker' >&2;exit 1
fi
echo 'P0_GATEWAY_SERVICE_WRITER_PREFIX_SCOPED_AND_DELETE_DENIED'
echo 'P0_GATEWAY_PRIVATE_OBJECT_UNCHANGED_AFTER_BYPASS_ATTEMPTS'
