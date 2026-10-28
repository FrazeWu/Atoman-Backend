# Encrypted off-host backup: preparation and activation boundary

## Current implementation

`scripts/ops/prepare-encrypted-backup.py` exports an existing, completed and
restore-verified PostgreSQL snapshot. It checks database file sizes and SHA256
values, includes the global roles archive and recovery manifests, and streams a
tar directly into `age`. It does not write an additional plaintext tar file.

Only native age **public** recipients are accepted. The production recovery
identity must be generated and retained independently of the VPS; do not store
it in `/etc/atoman`, environment variables, Git, logs or the backup bucket.
Disposable test identities are used only for synthetic unit-test fixtures.

The output directory is private (0700), ciphertext and receipt are 0600.
Incomplete encryption is removed; symlinks, path traversal, unverified restores,
changed input hashes and secret-key recipient strings fail closed. The command
shares the local backup producer's lock to avoid a concurrent retention race.

Example, after the owner has supplied a separately retained public recipient:

```sh
python3 scripts/ops/prepare-encrypted-backup.py \
  --source /var/backups/atoman/postgres/COMPLETED_VERIFIED_SNAPSHOT \
  --output-directory /var/lib/atoman-backup/encrypted-exports \
  --recipient OWNER_PROVIDED_AGE_PUBLIC_RECIPIENT
```

The command is preparation only. It deliberately performs no network requests,
reads no application storage credentials, does not select an existing bucket,
and does not enable a scheduled upload. Its receipt always records
`offsite_verified: false`. A local encrypted file is not an offsite backup.

## Required before activating remote transfers

The owner must identify an approved private destination and independently held
recovery key. Use a dedicated storage account/path or bucket with least-privilege
credentials, not the application's public asset bucket or reused application
credentials. Do not request secrets in chat; configure credentials through the
approved administrative channel.

For each transfer, verify the destination, upload only ciphertext, download a
verification copy and compare SHA256. Perform a real decryption and restore on a
separate authorized machine using the independently retained identity. Record
remote object identity, transfer result and recovery evidence before claiming
that an offsite backup is working. Avoid remote deletion until the owner's
retention policy has been selected and tested. Preserve local backups on upload
failure and surface failure distinctly from local snapshot success.

The current export covers PostgreSQL data, its global roles and manifests, not
all R2 objects, paper source files or runtime credentials. Those require separate
explicitly scoped backup coverage. Tests of synthetic encryption fixtures do not
prove that any production backup has been uploaded or restored elsewhere.

## Recovery guidance

On the independent recovery machine, validate the transferred ciphertext against
the recorded checksum, decrypt with `age --decrypt --identity` into a private
working directory, inspect the tar member list, and verify `payload-manifest.json`
after extraction. Do not restore directly into production. Restore roles and
privileges deliberately; the local database verification normalizes ownership.
The holder of the recovery identity must retain a second independently protected
copy: loss of the identity makes the encrypted backups unrecoverable.

## Docker maintenance notes

Before an engine package transition, retain exact previous packages, private
container/network/configuration metadata and a fresh database snapshot. Record
running and stopped containers separately. Preserve the existing storage driver
and volumes; do not run prune, volume removal or automatic storage migration.
After installation, compare container identities, images, mounts, published
bindings and restart policies against the pre-change inventory. Validate local
application readiness, older management-client API compatibility and an isolated
database restore. Keep SSH and the ingress proxy outside the package change.

A TCP connect reported by a local traffic proxy is not proof that the remote
service accepted it. External port checks need application-protocol responses and
positive controls. A routed probe does not substitute for a same-L2 regression
test. Record any administrative-tool restrictions on direct firewall inspection
rather than working around those restrictions.
