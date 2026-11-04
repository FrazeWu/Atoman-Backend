#!/bin/sh
# Disposable, loopback-only Stalwart v1 bootstrap smoke test.
# No production mail/DNS ports or real R2 credentials are used.
set -eu
name=atoman-mail-stalwart-bootstrap-smoke
image=stalwartlabs/stalwart:v1
port=18580
if docker container inspect "$name" >/dev/null 2>&1; then
  echo 'Refusing to modify an existing Stalwart smoke container' >&2
  exit 2
fi
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM
if ! docker image inspect "$image" >/dev/null 2>&1; then
  docker pull "$image" >/dev/null
fi
docker run -d --rm --init --name "$name" \
  -p "127.0.0.1:$port:8080" \
  --tmpfs /etc/stalwart:rw,nosuid,nodev,size=64m \
  --tmpfs /var/lib/stalwart:rw,nosuid,nodev,size=128m \
  "$image" >/dev/null
count=0
while [ "$count" -lt 30 ]; do
  if curl -fsS -o /dev/null -m 2 "http://127.0.0.1:$port/admin" 2>/dev/null; then
    break
  fi
  count=$((count+1))
  sleep 1
done
if ! curl -fsS -o /dev/null -m 3 "http://127.0.0.1:$port/admin" 2>/dev/null; then
  echo 'Stalwart localhost bootstrap readiness FAILED' >&2
  exit 1
fi
actual=$(docker port "$name" 8080/tcp)
if [ "$actual" != "127.0.0.1:$port" ]; then
  echo "Unexpected listener $actual" >&2
  exit 1
fi
printf '%s\n' 'STALWART_BOOTSTRAP_READY_LOOPBACK_ONLY'
