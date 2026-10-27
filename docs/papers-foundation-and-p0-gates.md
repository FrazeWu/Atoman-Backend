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

1. PostgreSQL and MinIO are published on 0.0.0.0:5432, 9100, 9101 and UFW shows public allow rules. Confirm cloud firewall and external reachability, then safely restrict Docker publication without breaking existing clients.
2. Backend and music worker systemd services run as root without hard CPU/memory ceilings. Scope and verify least-privilege service identities before introducing execution workers.
3. gVisor runsc, TeX Live and latexmk are not currently installed. Prove sandbox isolation, network denial, filesystem mounts, process limits, termination and failure recovery before enabling a compile endpoint.
4. Keep environment acquisition (controlled network), environment assembly (offline), and compilation (offline) in separate privileged domains.
5. Never expose a fake compile success or run TeX inside the API process.

## Local-only verification

```sh
cd /root/Atoman/.worktrees/papers-foundation-20261027-backend
make test-postgres-up
go test ./internal/modules/papers ./internal/app ./internal/migrationrunner ./docs
go build ./...
make swagger-check
make test-postgres-down

cd /root/Atoman/.worktrees/papers-foundation-20261027-frontend
bun install --frozen-lockfile
bun run type-check
bun run build
bunx vitest run tests/unit/router/papersRoutes.spec.ts tests/unit/router/routes.spec.ts
```

Do not execute the development-schema test migration against the production database. The test PostgreSQL container uses loopback 127.0.0.1:55432 and tmpfs storage.

The Papers API uses invitation checks on every private route, regardless of frontend route visibility or site-access flags.
