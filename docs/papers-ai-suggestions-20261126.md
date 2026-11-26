# Papers AI suggestions - first release

This release adds an owner-initiated, read-only model suggestion endpoint. It does not implement autonomous agents, provider tools, persistent chat history, cross-file writes, or changes while CRDT collaboration is open.

## Operator configuration

- The default is disabled. Set `PAPERS_AI_ENABLE=true` in the backend service environment only after release review.
- The built-in HTTPS base is `https://api.openai.com/v1`, using Chat Completions. Additional compatible bases may be listed, comma-separated, in `PAPERS_AI_ALLOWED_BASE_URLS`. Approve only trustworthy providers that accept the declared protocol. Users select from the resulting server allowlist. Arbitrary user-controlled outbound hosts are not supported.
- No API key is configured in the backend environment. Each user supplies their own key for an explicit request; the browser retains it only in the open panel's memory. Do not enable request-body logging or upstream request/response capture for these routes.
- Use private/no-store responses and the existing authenticated, CSRF-protected API. The feature is restricted to current active project owners with a valid Papers module grant. Editors and viewers cannot send shared manuscripts to providers through this endpoint.
- All outbound requests are HTTPS; DNS results must be public and the dial uses the checked IP, preserving TLS host verification. Redirects and environment proxies are not used. There is a 45-second deadline, at most four simultaneous calls per backend instance and at most one per owner.

## Data and mutation boundaries

Only the selected draft/selection, the explicitly previewed saved context files with exact revisions, and the instruction are sent. Source <= 48 KiB, source plus additional context <= 96 KiB, at most four additional files each <= 32 KiB, instruction <= 4000 UTF-8 bytes, output <= 48 KiB, provider generation limit 4096 tokens. The API never writes manuscript contents, stores credentials, or logs provider bodies. Model responses are untrusted plain text; revision and permission checks are enforced. The frontend presents the proposed replacement before a user accepts it. Acceptance uses a normal CodeMirror edit transaction, preserving undo and the existing optimistic save checks. Close or scope change aborts local waiting and clears credentials; it cannot retract a request already received by a provider or guarantee no charge. Provider retention and model availability are governed by that provider and the user's API account.

## Acceptance

Run Papers unit tests against an isolated PostgreSQL fixture, transport-policy tests with a synthetic RoundTripper, and the frontend browser acceptance with a mocked API. Verify a live provider with an authorized test account and a synthetic non-confidential document only; never use existing customer manuscripts or production user credentials for smoke tests. No database migration is required for this release.

Rollback: set `PAPERS_AI_ENABLE=false` and restart the backend using the normal managed release procedure. The ordinary editor and CRDT functionality are unchanged.
