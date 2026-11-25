# Papers collaboration: access foundation and durable synchronization contract

## Stage 8A: private shared projects (implemented)

Reuse Atoman session authentication and existing Papers module access grants.
A project owner can explicitly grant viewer or editor access to an existing
eligible account. Members never inherit ownership or administrative powers.
Only owners may manage members, file structure, snapshots, historical versions,
ZIP export and compilation. Editors may save existing text through the
existing revision-checked endpoint; viewers may read current project files.
Project removal clears memberships in the same transaction. Restoring a
trashed project therefore does not silently re-share it.

The project row is the serialization boundary for membership changes, reads,
existing file saves, structural mutations, snapshot restore and compilation
capture. A racing membership removal or downgrade is rechecked after acquiring
the row lock. Each accepted write creates an immutable file revision attributed
to the authenticated editor. Concurrent edits to the same base revision have
one accepted writer and a conflict for the other; the client keeps the local
draft. Sharing does not bypass the existing module invitation or limits.

This stage is NOT realtime collaborative text merging. The UI explicitly says
so. Members refresh clean source to see other saved revisions. The project
limit is 20 additional members per project. A revoked user cannot be prevented
from retaining content that was legitimately read before revocation.

## Stage 8B: authoritative Yjs synchronization (design, not enabled)

Existing Atoman frontend already has Yjs, y-websocket, y-codemirror.next,
useEditorCollaboration, and a shared CodeMirror editing layer. The Go
internal/collab Hub is a peer relay; it does not own a Y.Doc and drops updates
for slow clients. Its current browser-side seed-on-sync behavior is not a
durable source of truth for a thesis. Do not route private Papers rooms through
the existing blog UUID authorization handler or seed from a local draft.

Extend the shared collaboration layer to accept a typed resource namespace
and a resource-specific authorization adapter. Papers room identity should
bind project ID, text file ID and a server-issued editing epoch. Never accept
an arbitrary room ID as authority. Gate socket upgrade, read/update messages,
and reconnect through current account, module grant, membership, project/file
existence, role and epoch checks. Viewers must not send document updates.
Revocation/downgrade must invalidate live sessions, including reconnects.
Awareness metadata is transient and must not be mistaken for authorization.

A server-authoritative Y.Doc service using the already adopted Yjs protocol
is required. Consider a small internal Yjs runtime behind the authenticated Go
gateway; its exact deployment topology requires separate review. Seed once
from the accepted canonical revision under a database fence. Persist accepted
updates or an equivalent durable checkpoint before acknowledging them. Bound
message sizes, room count, update history, document size, and resource usage;
support deterministic replay and compaction. A relay or localStorage alone is
not an acceptable durable store. Reconnect must use state vectors, not whole
document replacement; slow consumers must reconnect/resynchronize rather
than silently diverge. Authenticate session identity independently of awareness.

Integrate the accepted text with existing PaperFile / PaperFileVersion and
project head revisions. Use a single transaction or an explicitly durable
fenced reconciliation protocol so compilation snapshots, history and named
checkpoints only consume a consistent accepted revision. While a collaborative
epoch owns a file, ordinary full-text PUT, project-wide replace, file rename,
delete and checkpoint restore must either be coordinated through that authority
or fail closed. Never accept a browser-provided Yjs snapshot as trusted source
without validation. A restore must close/fence the old epoch and initialize
a new epoch from the restored canonical revision. Offline updates belonging
to the old epoch may be exported for manual recovery but never replayed into
the new epoch automatically.

Release gates: two independent authenticated browsers concurrently inserting
and deleting Unicode text; cold restart and reconnect replay; duplicate/reordered
updates; viewer write rejection; membership revocation while connected; cross
project/file isolation; stale epoch rejection; quota exhaustion; compile,
snapshot and restore fencing; persistence failure and slow-client handling.
Existing blog collaboration regression and Papers editing/compile tests must
remain green. Keep this feature disabled until these gates are implemented.

## Stage 8.2: durable CRDT store (internal implementation)

The new `internal/platform/paperscrdt/runtime` uses the same pinned Yjs
13.6.33 release resolved by the frontend. Its lockfile pins dependencies;
`npm ci --ignore-scripts` builds no native dependency or package hook.
The Go adapter invokes a single trusted script by absolute path, passes
only bounded JSON on stdin, strips inherited service credentials, discards
decoder stderr and enforces a 5-second deadline, 96 MiB old-space limit
and two concurrent child processes per Engine instance. The store also suppresses its own SQL-value logging and returns sanitized database errors. This is a prototype
runtime boundary, not a security sandbox. It has no public network listener.

The `paper_collaborative_documents` table stores one compact full Yjs state
and state vector per active text file, with SHA-256 checks, a random epoch,
canonical file revision/path/digest and monotonic checkpoint sequence.
Each open initializes one authoritative state from the accepted source;
simultaneous opens race by compare-and-swap, rather than seeding duplicates.

A merge first reads the authorized state, then executes the decoder outside
the database transaction/lock. The commit reacquires the project row lock,
rechecks active account and grant (shared grant-row lock against revocation),
membership, file identity, revision/digest, epoch and checkpoint sequence.
Two competing merges may return a retry conflict; the gateway must retry
the same binary update against the latest checkpoint, never reseed it.
Accepted CRDT state, canonical source, immutable author-attributed file
version and project revision commit atomically. Text-neutral state changes
advance checkpoint sequence without inventing a text version. Duplicate
updates are idempotent. Delete-only updates are compared by full checkpoint,
not merely state vector, which may be identical after a deletion. The
engine rejects unresolved causal dependencies, malformed encodings, other
shared roots, rich text, embeds, over-limit data, and text mismatches.

Limits in this prototype: 256 KiB per incoming update, 1 MiB UTF-8 text,
4 MiB checkpoint per file, 64 KiB state vector, 16 MiB total checkpoint
storage per project and up to 64 checkpoint files. Existing canonical
project source limits still apply. A quota error never evicts an accepted
active checkpoint or overwrites a newer revision.

Ordinary source saves, structural changes and project snapshot restoration
can change the canonical revision and thereby invalidate a room's epoch;
project trash explicitly removes its checkpoints in the deletion
transaction. A new epoch starts from canonical source only. Deleted-file
checkpoints are reclaimed during subsequent initialization. No public
collaboration session currently exists, so legacy operations are unchanged.
Before enabling live rooms, the gateway must fence or coordinate these
legacy operations and proactively notify/close stale rooms, so unaccepted
offline edits are never lost or silently applied to a different epoch.

Run `./scripts/test-papers-crdt.sh` to exercise real Yjs and disposable
PostgreSQL. The regular Go suite does not require Node.js or network package
installation; actual Yjs integration tests explicitly skip without the
`PAPERS_CRDT_TEST_RUNTIME` setting.

Remaining release blockers: separate unprivileged runtime isolation,
bounded globally shared admission and per-user rate limits, authenticated
WebSocket gateway and state-vector delta exchange, client-side merge/retry
and offline-draft handling, active-session revocation, bounded checkpoint
growth/epoch compaction, performance/load testing, and browser/production
release gates. Do not expose the internal store via HTTP/WebSocket before
these gates. No worker dependency, frontend code, API routes or production
service settings are changed in stage 8.2.

## Stage 8.3A: isolated runtime transport

The Linux `papers-crdt-worker` command has no HTTP listener or database
configuration. A root-owned systemd socket passes exactly one AF_UNIX
listener via socket activation; the worker refuses root execution or a
missing/incorrect inherited descriptor. The socket is mode 0660, group
atoman. The worker verifies the backend account UID with SO_PEERCRED before
reading a request, and the backend UnixClient verifies both socket-file
ownership and the listener creator's UID. The IPC protocol accepts only
bounded length-prefixed JSON for the existing seed, merge and read
operations. Errors are sanitized, and the worker admits at most two
requests at once; excess connections are closed without allocating another
request buffer. Each request has an 8-second IPC deadline, including the
existing 5-second decoder deadline.

The candidate production service runs under DynamicUser, not the web
backend identity. It receives no database credentials. Its release lives
in a separately root-owned, read-only /opt/atoman-papers-crdt directory.
The service has a private network namespace and permits AF_UNIX only,
with IPAddressDeny=any, no capabilities or privilege escalation, a
read-only system filesystem, 512 MiB memory, 48 tasks, one CPU's quota,
and no access to application config/data directories or home directories.
A read-only private /run filesystem hides the host's filesystem sockets
while the inherited socket descriptor remains usable. Masking all of /run
using InaccessiblePaths instead conflicts with systemd namespace setup on
the tested VPS; do not replace the private mount with a less restrictive
configuration.

Run `./scripts/test-papers-crdt-isolation.sh` on the authorized Linux
development host with systemd, Docker, Node, Go, and the existing atoman
account. It builds temporary artifacts, creates a unique transient
systemd socket/service using the actual isolation properties, verifies
both Unix peer authorization and the filesystem/network restrictions
from inside the actual decoder process, and exercises the PostgreSQL
transaction store over the private Unix socket with disposable test data.
It removes its test service, socket, database container and staged files
on exit. This does not install or start the candidate production unit.

The deploy/papers/production candidate units require the explicit
CRDT_RUNTIME_APPROVED marker. Do not create it or install/enable the units
until the remaining release gates are completed. The IPC worker alone
does not authorize end users: the web backend remains the sole authority
for account, membership, file identity, epoch, checkpoint sequence and
canonical source. No public route is enabled, and the existing blog Hub
and CodeMirror behavior are unchanged. The next task is the authenticated
WebSocket gateway and browser protocol, including bounded per-user
admission, state-vector synchronization, active revocation and stale
epoch handling. Load and deployment tests remain required.

## Stage 8.3B1: authenticated durable WebSocket gateway

The gateway is opt-in through startup-only dependency injection.
NewService and the existing production API composition do not enable it.
The authenticated capability endpoint returns enabled=false by default,
and the WebSocket endpoint refuses upgrades when the gateway is absent.
No production runtime configuration or release marker is modified.

The file-scoped routes use the existing Atoman session middleware,
Papers access grant, project membership, and shared WebSocket upgrader.
A new reusable content-free room signal registry in internal/collab
coalesces notifications. Blog collaboration remains unchanged. A room
key includes both project and file identifiers and is never supplied
as an arbitrary client authorization string.

Wire protocol papers-crdt-v1 uses bounded JSON WebSocket text frames.
Initial state messages carry an epoch, monotonic checkpoint sequence,
canonical file/project revisions, role, and base64 full Yjs update/state
vector. The client sends a type=update frame with UUID request_id, epoch
and base64 Yjs delta; type=sync requests a fresh authorized checkpoint.
The server acknowledges an accepted update with a state message carrying
the corresponding request_id only after the database transaction commits.
Peers receive content-free internal signals and reread the authoritative
checkpoint under their own current authorization. No uncommitted client
update is broadcast. A retryable error includes the request_id and a
machine-readable code; resend the same delta only within the same epoch.
Malformed/oversized frames, stale epochs, and viewer writes terminate
the connection. A viewer can read an existing epoch but cannot seed one.

Each socket is associated with its authenticated database session ID.
The session and current account auth_version are checked under locks
inside the same transaction as collaboration reads and accepted writes.
The gateway checks current role, membership, grant, session, file
revision and epoch at every state publication and during 2-second
metadata polling. Polling does not load the full manuscript/checkpoint
unless a change must be sent. Role changes and expired/revoked sessions
close the connection on detection; no claim of instantaneous revocation
across all network buffers is made. Ordinary edits and rollback fence
previous epochs, and reconnects explicitly naming a stale epoch fail.
New epochs never automatically receive old pending deltas.

Prototype limits: 12 connections per gateway process, 3 per account,
6 per file, 2 simultaneous engine requests, 384 KiB incoming frame
limit (256 KiB decoded update), one queued request per reader, 150 ms
minimum interval between client application messages, bounded operation
and write deadlines, ping/pong, and a 30-minute connection lifetime.
Notification queues retain at most one signal per socket, not content.
These limits require load and multi-process deployment review before
public release. State messages currently contain full compact checkpoints;
state-vector differential downloads, offline recovery UI, presence/cursors
and CodeMirror client binding remain subsequent tasks. There is no public
activation or claim of full Overleaf-style collaboration at this point.

Test the gateway with real Yjs and isolated PostgreSQL using
`PAPERS_CRDT_TEST_RUNTIME` as in Stage 8.2, then
`./scripts/test-isolated.sh ./internal/modules/papers ./internal/collab -run 'TestCollab|TestRoomSignals|TestDurableCollab' -count=1`.
The separate isolation script also exercises the same tests through the
real restricted Unix worker. Normal production does not configure this
gateway; deployment is blocked until client, reconnect, revocation and
compile/restore integration acceptance gates are met.
