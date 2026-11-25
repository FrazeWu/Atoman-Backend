#!/bin/bash
# Operator-local candidate build only. No deployment, credentials, source
# projects, signing keys, or production config are touched.
set -euo pipefail
umask 077
test "$(id -u)" = 0
base=atoman-papers-texlive:academic-verified-20261124
expected=sha256:2738e65c08d33592eb5bdc14cd46f3662c89f6f611a466b2e92f27c8b97c6173
test "$(docker image inspect "$base" --format '{{.Id}}')" = "$expected"
repo="$(cd "$(dirname "$0")/../../.." && pwd)"
out="$(mktemp -d /var/tmp/atoman-papers-p0-fullenv.XXXXXXXX)"
tag=atoman-papers-texlive:academic-cjk-candidate-20261125
echo "CANDIDATE_DIR=$out"
echo "CANDIDATE_TAG=$tag"
docker build --pull=false --progress=plain -t "$tag" "$repo/scripts/papers/academic-cjk"
cid=""
trap 'if [ -n "$cid" ]; then docker rm -f "$cid" >/dev/null 2>&1 || true; fi' EXIT
docker image inspect "$tag" --format '{{.Id}}' > "$out/image-id"
docker run --rm --network none --entrypoint /bin/sh "$tag" -c 'tlmgr info --only-installed --data name,localrev' > "$out/installed-packages.txt"
docker run --rm --network none --entrypoint /usr/bin/dpkg-query "$tag" -W fonts-noto-cjk > "$out/installed-fonts.txt"
cid="$(docker create --network none "$tag" /bin/true)"
docker export "$cid" > "$out/texlive-rootfs.tar"
docker rm "$cid" >/dev/null; cid=""
cd "$repo"
PAPERS_P0_NORMALIZE_CONFIRM=1 go run ./cmd/papers_p0_normalize_texlive "$out"
sha256sum "$out/normalized-rootfs.tar" "$out/installed-packages.txt" "$out/installed-fonts.txt"
stat -c 'archive_bytes=%s' "$out/normalized-rootfs.tar"
rm "$out/texlive-rootfs.tar"
echo "CANDIDATE_READY=$out"
