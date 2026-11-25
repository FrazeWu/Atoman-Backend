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
