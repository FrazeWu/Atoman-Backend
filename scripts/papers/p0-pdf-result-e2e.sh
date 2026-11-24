#!/usr/bin/env bash
# Isolated acceptance. Compiles fixed benign TeX, then tests real private MinIO,
# queue commit and authenticated download. Not a production deployment.
set -Eeuo pipefail
umask 077
[[ "${PAPERS_P0_PDF_ACCEPTANCE:-}" == 1 ]] || exit 2
root="$(mktemp -d /var/tmp/atoman-papers-pdf-test.XXXXXXXX)"
suffix="$(od -An -N5 -tx1 /dev/urandom | tr -d ' \n')"
tex_name="papers-pdf-fixture-$suffix"
minio_id=''
export_id=''
cleanup(){
 local rc=$?
 trap - EXIT INT TERM
 set +e
 docker rm -f "$tex_name" >/dev/null 2>&1 || true
 if [[ -n "$minio_id" ]];then docker rm -f "$minio_id" >/dev/null 2>&1 || rc=1;fi
 if [[ -n "$export_id" ]];then docker rm -f "$export_id" >/dev/null 2>&1 || rc=1;fi
 # Never delete a directory that could still contain a live sandbox/mount.
 if [[ -d "$root/runs" ]] && [[ -n "$(find "$root/runs" -mindepth 1 -maxdepth 1 -print -quit)" ]];then
   echo "Preserving test-owned incomplete sandbox for inspection: $root" >&2
   exit 1
 fi
 if awk -v p="$root" '$5==p || index($5,p"/")==1 {found=1} END{exit !found}' /proc/self/mountinfo;then
   echo "Preserving test-owned mount: $root" >&2
   exit 1
 fi
 case "$root" in /var/tmp/atoman-papers-pdf-test.*) rm -rf -- "$root";; *) rc=1;;esac
 echo 'ISOLATED_PDF_TEST_CLEANUP'
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cd "$(dirname "$0")/../.."
readonly tex='texlive/texlive@sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4'
readonly image='minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e'
docker image inspect "$tex" "$image" >/dev/null
if [[ "${PAPERS_P0_LIVE_PDF_CONFIRM:-}" == 1 ]];then
 [[ "$(id -u)" == 0 ]] || exit 2
 [[ "$(df -Pk /var/tmp | awk 'NR==2{print $4}')" -ge 15728640 ]] || exit 2
 chgrp 65534 "$root"
 chmod 0710 "$root"
 mkdir -m 0700 "$root/bin" "$root/runs"
 chgrp 65534 "$root/runs"
 chmod 0710 "$root/runs"
 readonly runtime_url="https://storage.googleapis.com/gvisor/releases/release/20261019.0/x86_64/gvisor.tar.zstd"
 readonly runtime_digest="bcbf431b2f9a039f6ab23bfe23986031130c0f149294978cedfd46910901deba0737d8642c848f597ed4b5c72324885db4d75ee86a1f6a90915fbe1f2f2eb915"
 curl -fsSL --retry 2 --connect-timeout 15 --max-time 180 "$runtime_url" -o "$root/gvisor.tar.zstd"
 printf '%s  %s\n' "$runtime_digest" "$root/gvisor.tar.zstd" | sha512sum -c -
 tar --zstd -xf "$root/gvisor.tar.zstd" -C "$root/bin"
 "$root/bin/runsc" --version | head -2
 export_id="$(docker create --label atoman.test=isolated "$tex" /bin/sh)"
 docker export "$export_id" >"$root/texlive-rootfs.tar"
 docker rm "$export_id" >/dev/null
 export_id=''
 export PAPERS_P0_LIVE_WORKROOT="$root/runs"
 echo LIVE_PINNED_IMAGE_AND_RUNTIME_READY
fi
timeout 60 docker run --rm --name "$tex_name" --network none --read-only --user 65534:65534 --cap-drop ALL --security-opt no-new-privileges --memory 512m --memory-swap 512m --cpus 1 --pids-limit 128 --tmpfs /tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777 -e HOME=/tmp -e TEXMFVAR=/tmp/texmf-var -e TEXMFCONFIG=/tmp/texmf-config --entrypoint /bin/sh "$tex" -c 'set -eu; cd /tmp; printf "%s\n" "\\documentclass{article}" "\\begin{document}" "Atoman Papers acceptance fixture. Test formula: \$E=mc^2\$." "\\end{document}" >main.tex; pdflatex -no-shell-escape -interaction=nonstopmode -halt-on-error main.tex >&2; cat main.pdf' >"$root/fixture.pdf"
test "$(head -c 5 "$root/fixture.pdf")" = '%PDF-'
export PAPERS_P0_PDF_FIXTURE="$root/fixture.pdf"
echo "TEX_FIXTURE_BYTES=$(wc -c <"$PAPERS_P0_PDF_FIXTURE")"
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
minio_id="$(docker run --detach --rm --name "papers-pdf-minio-$suffix" --label atoman.test=isolated --security-opt no-new-privileges --cap-drop ALL --tmpfs /data:rw,size=256m -e MINIO_ROOT_USER="$PAPERS_P0_IAM_ADMIN_USER" -e MINIO_ROOT_PASSWORD="$PAPERS_P0_IAM_ADMIN_SECRET" -p 127.0.0.1::9000 "$image" server /data --address :9000)"
binding="$(docker port "$minio_id" 9000/tcp)"
[[ "$binding" =~ ^127\.0\.0\.1:([0-9]+)$ ]] || exit 1
export PAPERS_P0_IAM_ENDPOINT="http://127.0.0.1:${BASH_REMATCH[1]}"
ready=0
for i in $(seq 1 80);do if curl -fsS --max-time 2 "$PAPERS_P0_IAM_ENDPOINT/minio/health/ready" >/dev/null 2>&1;then ready=1;break;fi;sleep .25;done
test "$ready" = 1
export AWS_ACCESS_KEY_ID="$PAPERS_P0_IAM_ADMIN_USER"
export AWS_SECRET_ACCESS_KEY="$PAPERS_P0_IAM_ADMIN_SECRET"
export AWS_DEFAULT_REGION=us-east-1 AWS_EC2_METADATA_DISABLED=true
aws --endpoint-url "$PAPERS_P0_IAM_ENDPOINT" s3api create-bucket --bucket "$PAPERS_P0_IAM_BUCKET" --object-lock-enabled-for-bucket >/dev/null
aws --endpoint-url "$PAPERS_P0_IAM_ENDPOINT" s3api put-object-lock-configuration --bucket "$PAPERS_P0_IAM_BUCKET" --object-lock-configuration '{"ObjectLockEnabled":"Enabled","Rule":{"DefaultRetention":{"Mode":"COMPLIANCE","Days":1}}}' >/dev/null
(cd scripts/papers/p0_minio_iam && go build -o "$root/iam" .)
"$root/iam"
unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY PAPERS_P0_IAM_ADMIN_USER PAPERS_P0_IAM_ADMIN_SECRET PAPERS_P0_IAM_PUBLISHER_USER PAPERS_P0_IAM_PUBLISHER_SECRET
unset TEST_POSTGRES_DSN TEST_DATABASE_URL TEST_POSTGRES_TEMPLATE_DATABASE REFERENCE_TEST_POSTGRES_DSN
GO_TEST_PARALLELISM=1 GOMAXPROCS=2 bash scripts/test-isolated.sh ./internal/modules/papers -run '^TestP0RealPDFStorageAndAuthenticatedDownload$' -count=1 -v
echo PDF_STORAGE_E2E_OK
