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
