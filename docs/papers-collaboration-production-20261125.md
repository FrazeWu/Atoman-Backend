# Papers production collaboration release gate (2026-11-25)

## State at inspection

8.5 source and regression acceptance completed and both remote main branches
were verified. Backend base 2bf9a44; frontend base 6837c680. This is a
preparation task, not authority to activate production.

Read-only inspection of the production host found:

- Existing Atoman backend (atoman:atoman), compile launcher, snapshot gateway,
  and PDF gateway active. Compile queue is embedded in the backend, not a
  separate worker service. Public HTTPS root returned 200.
- Private storage and earlier security review markers exist; CRDT approval
  marker, CRDT service/socket and live CRDT executable/runtime are absent.
- Existing trusted compilation pin is distinct from the local academic CJK
  candidate. The 1,602,460,160-byte candidate archive remains local. Do not
  repin or enable XeLaTeX/LuaLaTeX until its separate signed release procedure
  is verified; keep existing trusted compilation available.
- Scheduled backup and mail backup health services last reported success.
  That does not attest to a restore of the current backup. Require a dated
  successful recovery verification before migration.
- The canonical backend checkout contains unrelated uncommitted mail work.
  Use a clean, exact release commit and the managed-runtime deployment
  procedure; never stash, overwrite or include that work.

## Independent startup approval

The new Linux startup path is default-off. Compilation and collaboration are
independent. Collaboration requires all of:

1. Explicit PAPERS_COLLABORATION_ENABLE=1 in an operator-managed drop-in.
2. The separately reviewed root-owned 0600 approval marker, delivered through
   systemd LoadCredential as papers-crdt.json in the protected
   atoman-backend.service credential directory. Its schema is
   `{"approved":true,"protocol":"papers-crdt-v1"}`.
   This is an example of the approved format, not an approval to create it.
3. The existing non-root atoman:atoman backend identity, expected PostgreSQL
   schema, and the fixed root-owned /run/atoman-papers-crdt.sock listener.
4. A successful bounded seed/read round-trip using synthetic source, with
   exact text, state and state vector consistency. The startup probe creates
   no database documents.

An absent or failed collaboration gate leaves the ordinary editor and
existing compile worker unchanged; a generic diagnostic identifies the
failure without exposing credentials or manuscript contents. The gateway
is registered disabled until successful startup. A feature gate change
requires a controlled backend restart, not a public configuration endpoint.

## Release order and required evidence

- Complete the isolated Worker, gateway, queue, migration, unit and browser
  acceptance, verify the exact signed/hashed release artifacts and retain
  their provenance. Review resource bounds and single-process presence
  behavior. Verify actual host systemd profiles under a disposable identity
  and a load test at the existing admission limits (12 total / 3 per
  account / 6 per room).
- Check a recent complete backup and verified isolated restoration of that
  backup. Rehearse the exact migration on a disposable restored database,
  including existing projects, history, grants and compile queue. Verify
  new member, snapshot, source map and collaboration tables/indexes. Never
  run migration against ambient production DB settings in testing.
- Coordinate a maintenance window and a clean release checkout. Use the
  established managed-runtime backend activation, including required
  companion mail/music binaries, and retain previous versions and frontend
  artifacts. Avoid modifying the dirty canonical mail worktree.
- First release migrations and compatible frontend/backend while
  collaboration remains disabled. Verify ordinary editor and compile
  compatibility, mail and other affected modules. Deploy the CRDT runtime
  as a separate root-owned read-only artifact with lockfile-pinned modules
  and the reviewed socket/service. Do not activate it until all release
  gates and the explicit operator approval are recorded.
- After approval, install the isolated units and dedicated drop-in, enable
  the socket, and perform the controlled backend restart. Confirm the
  protected startup probe, authenticated private project capability,
  two-account edit/reconnect/revoke/rollback, existing compilation, quotas,
  resource use and no uninvited access. Use only disposable invited test
  projects. Verify proxy upgrade and idle timeout without adding a public
  CRDT port. Confirm cleanup and ordinary site health.
- The first rollout remains single backend process. Presence and admission
  counters are process-local. Do not horizontally scale the gateway until
  distributed presence/admission is designed and tested.

## Rollback

If any gate fails before activation, leave collaboration disabled and
preserve the evidence. After activation, first remove/disable only the
collaboration drop-in and marker under the approved maintenance procedure,
restart the backend, and stop/disable the CRDT socket/service. Verify the
ordinary editor, existing compile worker and public health. Preserve
canonical sources, accepted revisions, history and CRDT checkpoint tables:
do not drop tables or restore over live data merely to roll back a feature.
If binary/frontend rollback is required, use the established managed runtime
rollback and verify compatibility with migrated data; a full DB restore is a
separate incident decision. Reverting an environment pin, publishing an
unsigned archive, or overwriting unrelated work is not an acceptable rollback.

Actual production configuration, files, credentials, signing keys, services
and database are unchanged by this task.

## 8.6B capacity and ingress acceptance

The current production PostgreSQL backup completion record dated
2026-11-25T04:33:56Z records a full isolated restore of 288 tables.
The 960,539,423-byte dump's SHA-256 was independently recomputed on the
host and matched the recorded digest. This verifies the existing backup
and recovery evidence, not a new restore or migration of that dump.
The exact candidate migration passed against disposable PostgreSQL.
A full migration rehearsal on the restored production backup remains
a separate pre-activation requirement; do not run it concurrently with
load acceptance or on a host without sufficient disk and memory headroom.

The initial capacity test admitted twelve sockets and rejected each
account, room and global overflow, but two of six concurrent writers
exhausted the existing browser retry budget. The gateway now serializes
seed/merge work by project and file before obtaining one of the two
global decoder slots. Admission bounds the number of waiters; no
unbounded background worker or queue is created. The reservation waits
at most four seconds and respects cancellation; the original ten-second
operation budget includes queue time. Database revision, epoch and
authorization checks remain authoritative. Different rooms can proceed
concurrently, and idle reservation state is reclaimed.

The production API Nginx template previously routed Papers collaboration
through the normal API proxy without WebSocket upgrade headers. The
candidate adds a narrowly matched nested location under the existing
API prefix, preserving the ordinary API and blog WebSocket paths.
The independent loopback test uses the candidate template, an actual
disposable Nginx process and a synthetic HTTP/WebSocket upstream. It
verifies a 101 upgrade and frame transfer, auth/subprotocol forwarding
and absence of upgrade forwarding on ordinary API/capability paths.
No live configuration is reloaded; external authenticated ingress
acceptance remains required during the controlled rollout.

The corrected isolated capacity test passed on 2026-11-25: 12
admitted connections in two rooms, all three admission overflow
checks, two waves of six concurrent writers (2.48 s and 2.44 s
maximum acknowledgements, zero retries), and a wave of twelve
concurrent writers (6.47 s maximum, four aggregate retries).
All 24 submitted synthetic deltas were present exactly once, the
final file revisions and sequences were verified on every socket,
and reservations/admission counts were reclaimed. These measured
values describe this small synthetic test on the current VPS,
not large-document or multi-process throughput guarantees.
