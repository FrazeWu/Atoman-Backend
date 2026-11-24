# Papers application and launch checkpoint

- Application, review, permission grant and durable approval-email queue implemented.
  Only the registered account email is used. Approval and grant are transactional.
  Retry uses a stable provider idempotency key; duplicate review is harmless.
- Backend application/review/approval mail tests and build passed. The mail
  transport was tested with a fixture, not a real recipient.
- Frontend commit 35fbb29b: Papers last in Topbar, applicant status, admin review,
  and private noindex route. 90 focused regression tests and build passed.
- PostgreSQL backup and actual restore verification completed:
  /var/backups/atoman/postgres/20261124T063805Z-dylg0jjx (COMPLETE);
  production 276 tables and development 231 tables checked.
- Dedicated production bucket atoman-papers-private-v1 created with 12 GiB
  hard quota, versioning and COMPLIANCE retention. Six scoped storage
  identities provisioned, shared-bucket metadata and list access denied.
- Fixed systemd's root:root 0440 credential compatibility, requiring the
  exact protected per-service readonly tmpfs. Ordinary group-readable
  credential files remain rejected.
- Signed pinned environment published:
  sha256:4dadd0958430ae37f550efd10d8da5c9a8a773961b0c43602e3cc7c2cdeb466c.
  One-shot signing credential removed after successful publication.
  Production snapshot and PDF publication gateways active.
- Main site remains operational. Public Papers route not activated.
  Production database role/config provisioning was not executed. The
  compile approval marker is absent; Worker and Launcher remain disabled.
  Do not advertise the application workflow or enable the compiler until
  the remaining database and full deployment acceptance gates are met.
