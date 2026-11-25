#!/usr/bin/env bash
# Disposable systemd socket-activation acceptance. No production data, API,
# database, credential files, service definitions or release markers are read.
set -euo pipefail
cd "$(dirname "$0")/.."
test "$(id -u)" = 0
id atoman >/dev/null
root="$(mktemp -d /opt/atoman-crdt-test.XXXXXXXX)"
unit="atoman-crdt-test-$(basename "$root" | cut -d. -f2)"
socket="/run/$unit.sock"
db_container=""
cleanup(){
 if [[ -n "$db_container" ]]; then docker rm -f "$db_container" >/dev/null 2>&1 || true; fi
 systemctl stop "$unit.socket" "$unit.service" >/dev/null 2>&1 || true
 systemctl reset-failed "$unit.socket" "$unit.service" >/dev/null 2>&1 || true
 rm -rf -- "$root"
}
trap cleanup EXIT
test ! -e "$socket"
chmod 0755 "$root"
mkdir "$root/runtime"
cp internal/platform/paperscrdt/runtime/{package.json,package-lock.json,engine.mjs,cli.mjs,test-peer.mjs} "$root/runtime/"
(cd "$root/runtime" && npm ci --ignore-scripts --no-audit --no-fund)
go build -o "$root/worker" ./cmd/papers_crdt_worker
go test -c -o "$root/socket-tests" ./internal/platform/paperscrdt
go test -c -o "$root/store-tests" ./internal/modules/papers
chmod -R a+rX,go-w "$root"
# Run a probe within the actual child process that decodes the synthetic
# manuscript. These access checks never read private file contents.
cat > "$root/runtime/probe.mjs" <<'PROBE'
import fs from 'node:fs'
import net from 'node:net'
import assert from 'node:assert/strict'
import {fileURLToPath} from 'node:url'
assert.notEqual(process.getuid(), 0)
assert.equal(process.env.DATABASE_URL, undefined)
assert.equal(process.env.NODE_OPTIONS, undefined)
for (const path of ['/etc/atoman','/opt/atoman','/var/lib/atoman','/root']) {
 assert.throws(()=>fs.accessSync(path,fs.constants.R_OK|fs.constants.X_OK))
}
assert.throws(()=>fs.writeFileSync('/run/must-not-write','fixture'))
assert.equal(fs.readdirSync('/run').some(name=>name.startsWith('atoman-')||name==='docker.sock'),false)
assert.throws(()=>fs.writeFileSync(fileURLToPath(new URL('./must-not-write',import.meta.url)),'fixture'))
await new Promise((resolve,reject)=>{
 const server=net.createServer()
 server.on('error',err=>{
  if (['EAFNOSUPPORT','EPERM','EACCES'].includes(err.code))resolve()
  else reject(err)
 })
 server.listen({host:'127.0.0.1',port:0},()=>{
  server.close()
  reject(Error('IP listener unexpectedly allowed'))
 })
})
await import('./cli.mjs')
PROBE
# Copy the actual production isolation properties to a unique transient unit.
# Startup dependencies, approval marker, release path and restart behaviour
# belong to real deployment and are intentionally not applied here.
properties=()
while IFS= read -r line; do
 case "$line" in
  DynamicUser=*|UMask=*|TimeoutStopSec=*|MemoryMax=*|TasksMax=*|CPUQuota=*|LimitNOFILE=*|NoNewPrivileges=*|CapabilityBoundingSet=*|AmbientCapabilities=*|ProtectSystem=*|ProtectHome=*|PrivateDevices=*|PrivateTmp=*|PrivateNetwork=*|ProtectKernelTunables=*|ProtectKernelModules=*|ProtectKernelLogs=*|ProtectControlGroups=*|ProtectProc=*|RestrictNamespaces=*|RestrictSUIDSGID=*|LockPersonality=*|RestrictRealtime=*|RestrictAddressFamilies=*|IPAddressDeny=*|SystemCallArchitectures=*|TemporaryFileSystem=*|InaccessiblePaths=*)
   properties+=( "--property=$line" );;
 esac
done < deploy/papers/production/atoman-papers-crdt.service
systemd-run --unit="$unit" --collect --no-ask-password --service-type=exec \
 "${properties[@]}" \
 --socket-property="ListenStream=$socket" \
 --socket-property=SocketUser=root --socket-property=SocketGroup=atoman \
 --socket-property=SocketMode=0660 --socket-property=Backlog=8 \
 --socket-property=RemoveOnStop=yes \
 "$root/worker" --node=/usr/bin/node --runtime="$root/runtime/probe.mjs" --client-user=atoman
test -S "$socket"
test "$(stat -c '%U:%G:%a' "$socket")" = root:atoman:660
runuser -u atoman -- env PAPERS_CRDT_TEST_SOCKET="$socket" PAPERS_CRDT_TEST_RUNTIME="$root/runtime" "$root/socket-tests" -test.run '^TestIsolatedSocketRuntime$' -test.v
# Root can traverse the socket ACL, but its peer UID is rejected by the
# runtime. An unrelated account cannot even connect to the socket.
CRDT_TEST_SOCKET="$socket" python3 - <<'PY'
import os,socket,struct,json
p=os.environ['CRDT_TEST_SOCKET']
with socket.socket(socket.AF_UNIX) as s:
 s.settimeout(2)
 s.connect(p)
 raw=json.dumps({'op':'seed','text':'unauthorized fixture'}).encode()
 try:
  s.sendall(struct.pack('>I',len(raw))+raw)
  assert s.recv(1)==b''
 except (BrokenPipeError,ConnectionResetError):
  pass
print('UNAUTHORIZED_PEER_REJECTED')
PY
runuser -u nobody -- env CRDT_TEST_SOCKET="$socket" python3 - <<'PY'
import os,socket
with socket.socket(socket.AF_UNIX) as s:
 try:s.connect(os.environ['CRDT_TEST_SOCKET'])
 except PermissionError:pass
 else:raise SystemExit('unrelated UID unexpectedly connected')
print('SOCKET_ACL_REJECTED')
PY
pid="$(systemctl show "$unit.service" -p MainPID --value)"
test "$pid" -gt 0
test "$(awk '/^Uid:/{print $2}' "/proc/$pid/status")" -ne 0
test "$(awk '/^Uid:/{print $2}' "/proc/$pid/status")" -ne "$(id -u atoman)"
test "$(awk '/^NoNewPrivs:/{print $2}' "/proc/$pid/status")" = 1
test "$(awk '/^CapEff:/{print $2}' "/proc/$pid/status")" = 0000000000000000
test "$(readlink "/proc/$pid/ns/net")" != "$(readlink /proc/1/ns/net)"
test ! -S "/proc/$pid/root$socket"
test ! -e "$root/runtime/must-not-write"
echo ISOLATED_CRDT_SOCKET_RUNTIME_VERIFIED
# Exercise the real transaction store through the same restricted worker.
# This container is owned by this invocation and binds only a random local
# port. Never use an ambient or production database configuration.
db_container="$(docker run --detach --rm --pull=never --label atoman.test=isolated \
 --name "$unit-db" --tmpfs /var/lib/postgresql/data --shm-size=256m \
 -e POSTGRES_USER=atoman_test -e POSTGRES_PASSWORD=atoman_test \
 -e POSTGRES_DB=atoman_test -p 127.0.0.1::5432 postgres:16-alpine)"
ready=0
for i in $(seq 1 40); do
 if docker exec "$db_container" pg_isready -U atoman_test -d atoman_test >/dev/null 2>&1; then ready=1; break; fi
 sleep 1
done
test "$ready" = 1
binding="$(docker port "$db_container" 5432/tcp)"
[[ "$binding" =~ ^127\.0\.0\.1:([0-9]+)$ ]]
dsn="postgres://atoman_test:atoman_test@127.0.0.1:${BASH_REMATCH[1]}/atoman_test?sslmode=disable"
runuser -u atoman -- env -i PATH=/usr/bin:/bin HOME=/nonexistent \
 TEST_POSTGRES_DSN="$dsn" TEST_DATABASE_URL="$dsn" \
 PAPERS_CRDT_TEST_SOCKET="$socket" PAPERS_CRDT_TEST_RUNTIME="$root/runtime" \
 "$root/store-tests" -test.run '^TestDurableCollab' -test.count=1 -test.v
echo ISOLATED_CRDT_DATABASE_TRANSACTION_VERIFIED
