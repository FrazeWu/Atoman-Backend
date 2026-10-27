# Atoman Papers: foundation slice and deployment gates

As of 2026-10-27, the Papers foundation is intended to run inside the existing Atoman API.

## Implemented in this slice

- Fail-closed, administrator-managed invited-user grant.
- Private project CRUD with stable owner identifiers and main.tex initialization.
- Text file creation, retrieval, listing, accepted-version history and optimistic concurrency control.
- API route registration under /api/v1/papers, with CSRF/session middleware inherited from Atoman.
- Swagger contract and access/isolation integration tests.
- No server-side LaTeX execution routes or AI Agent routes.

## Not yet implemented

- Immutable project snapshots and private binary upload.
- ZIP import/export, file rename/delete UI, project history restore.
- LaTeX environment builder, restricted toolchain, compiler, PDF preview.
- BYOK, AI persistence, inline review and autonomous agent.
- A production administrator interface for inviting users.

## P0 blockers to deployment of untrusted LaTeX

1. On the latest check, PostgreSQL now publishes only to 127.0.0.1:5432, but legacy UFW rules still allow port 5432; verify IPv4/IPv6 and cloud ingress before declaring it secured. MinIO remains published on 0.0.0.0:9100/9101 and corresponding UFW allow rules remain. Investigate actual authorized consumers and safely restrict the Docker publication without breaking existing clients.
2. Backend and music worker systemd services run as root without hard CPU/memory ceilings. Scope and verify least-privilege service identities before introducing execution workers.
3. A standalone gVisor runsc systrap smoke test (release-20261019.0) passed basic network, readonly-rootfs (with --overlay2=none), non-root identity and writable-output checks. It was **not installed as a global runtime**; TeX Live and latexmk remain uninstalled. Full TeX compilation, PID/memory enforcement, termination, mount escape and failure recovery tests are still required before enabling a compile endpoint. See [P0 systrap smoke report](papers-p0-systrap-smoke-20261027.md).
4. Keep environment acquisition (controlled network), environment assembly (offline), and compilation (offline) in separate privileged domains.
5. Never expose a fake compile success or run TeX inside the API process.

## Local-only verification

```sh
cd /root/Atoman/.worktrees/integrate-papers-20261027-backend
./scripts/test-isolated.sh ./internal/modules/papers ./internal/app ./internal/migrationrunner ./docs
go build ./...
make swagger-check

cd /root/Atoman/.worktrees/integrate-papers-20261027-frontend
bun install --frozen-lockfile
bun run type-check
bun run build
bunx vitest run tests/unit/router/papersRoutes.spec.ts tests/unit/router/routes.spec.ts
```

Do not execute test migrations against the production database. The scripts/test-isolated.sh helper starts its own disposable PostgreSQL container on a random loopback-only port with tmpfs storage and removes only the container belonging to that test run. Always let each test process complete before cleaning up its database.

The Papers API uses invitation checks on every private route, regardless of frontend route visibility or site-access flags.
