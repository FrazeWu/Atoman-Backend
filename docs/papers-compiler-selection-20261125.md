# Compiler selection candidate (2026-11-25)

Default and backward-compatible compiler: pdfLaTeX. The job has an immutable
compiler value, defaulted by the existing migration runner for historical
rows. Old snapshots omit the field and still mean pdfLaTeX. LuaLaTeX is
included in the digest-bound source snapshot and checked against the
authoritative job during staging. Idempotency rejects engine changes under
the same key.

LuaLaTeX is available only for the exact verified AcademicEnvironment pin
and with independently configured enable_lualatex=true in both protected
API and launcher configuration documents. These flags are false by
default. Deployment order must be migration, launcher, API, then frontend;
do not enable the flags until the actual signed archive and production
composition have passed independent release acceptance. Old jobs continue
to work with the existing sealed pdfLaTeX driver. XeLaTeX is absent from the
pinned archive and remains unavailable; adding it requires a separately
built, reviewed and signed environment.

LuaLaTeX uses a separate byte-verified fixed guest driver, no shell escape,
no socket library, a read-only rootfs and isolated source, no network,
the existing 768 MiB memory, one CPU, 256 PID, 2-minute wall-clock policy
and bounded output mount. It intentionally does NOT use --safer because
the shipped luaotfload explicitly fails with that option. Its cache is
inside the private quota-bounded /output, not shared and not part of the
source snapshot. Lua code itself is still executed inside the sandbox:
the sandbox is the essential boundary; these tests do not prove all
possible guest behaviors are safe.

The opt-in synthetic live test validates actual pinned archive compilation,
fontspec, Unicode, BibTeX, selected blocked process and socket operations,
source write denial, PDF output, SyncTeX, enforced limits and cleanup.
No production source, object credentials, DB or launcher socket is used.
