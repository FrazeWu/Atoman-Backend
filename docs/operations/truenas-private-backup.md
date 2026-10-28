# TrueNAS private encrypted offsite backup

## Verified deployment state (2026-10-28)

- TrueNAS: `172.16.10.201`, user/group `fa:fa`, destination
  `/mnt/basic_pool/AtomanBackups`, effective owner-only mode `0700`,
  approximately 38 TB available at initial verification.
- Dedicated NAS pull SSH identity is stored under
  `AtomanBackups/.ssh/vps_readonly_ed25519` (0600); it is separate from
  the Mac administration identity and separate from the age recovery key.
- VPS has a dedicated non-root `atoman_backupread` identity (nologin)
  chrooted to `/srv/atoman-backup-sftp`, with
  `ForceCommand internal-sftp -R -d /exports`; passwords, PTY, port
  forwarding, agent forwarding and writes are prohibited.
- SSH config passed `sshd -t`, root effective configuration remained
  unchanged, and NAS authenticated and listed the SFTP exports successfully.
  A real attempted upload from NAS was rejected.
- The private NAS identity pins the VPS's existing trusted ED25519 SSH host
  key. The NAS→VPS path requires
  `KexAlgorithms=curve25519-sha256` because the default large hybrid KEX
  stalled on the current network route. This is a modern, secure key exchange
  and is scoped to this connection only.
- The first ciphertext snapshot was transferred to
  `AtomanBackups/snapshots/20261028T033917Z-cuixo7o9` by the NAS-owned
  pull program. Its complete 753,950,616-byte ciphertext matches the original
  SHA256 `817e47fda32f180d8a3b3eed2574247ed81e59d6871187d558e609e5c7c06a43`.
  It has a NAS-local completion marker. **This is a confirmed offsite copy.**
- Independent decryption from the actual NAS copy succeeded on Mac, with
  matching ciphertext SHA256 and all five plaintext member checksums. The
  recovered `atoman_dev.dump` restored into a network-isolated, tmpfs-backed
  PostgreSQL 16 container; all 231 table row counts agreed with the source
  manifest. The temporary container was removed after verification. The full
  production dump was cryptographically verified but was not restored on Mac.
- TrueNAS middleware cron job ID `2` is **enabled** under `fa` and configured
  for every hour at minute 30 in `America/Los_Angeles`. It uses `flock` to
  prevent overlapping runs, never deletes NAS snapshots, and resumes partial
  downloads when the connection is interrupted.
- The newest published snapshot can take longer to download on the currently
  unstable WAN link. The script prefers newer published snapshots and fails
  closed if the newest independently verified **database source completion time**
  becomes older than 48 hours. Encryption/export-directory creation time is
  not an acceptable proxy for database recovery-point age.
- The encrypted payload manifest and its transfer receipt now include
  `source_snapshot_id` and the UTC `source_completed_at` from the completed,
  restore-verified PostgreSQL backup manifest. NAS completion markers carry
  this same provenance. Earlier exports require a separately verified
  one-time provenance annotation before being included in freshness reports.

## Encryption and recovery identity

Only the public age recipient is sent to VPS:

```text
age1rfgu75985jpk7aml06uvdzm0ewa4p7j9ua7l8vut3ek5eueddgfs542fe8
```

The private age recovery identity remains on the Mac at
`/Users/fafa/.config/atoman-backup/age-recovery-identity.txt`.
Never send its contents through chat or to the VPS/NAS pull account.
At the time of initial setup, Mac FileVault was disabled: arrange a second
physically separated, protected offline recovery-key copy and consider
enabling full-disk encryption.

The export contains verified PostgreSQL snapshots, role backup, and member
checksums. It does not automatically include Cloudflare R2 objects, external
media files, runtime secrets or other independent databases.

## Scheduling and remaining activation

Existing VPS `atoman-backup.timer` runs around 03:30 UTC (randomized 0–5
minutes), validates completed PostgreSQL dumps by isolated restoration, and
retains 14 local snapshots.

The new vetted `publish-offsite-backups.py` selects the newest completed,
restore-verified snapshot, encrypts it with the public age key, verifies the
ciphertext checksum and publishes only root-owned read-only files under the
SFTP chroot. It retains at most 10 **local VPS ciphertext cache entries**
after a successful new publication. It **never deletes NAS snapshots**
and never touches PostgreSQL data or its backup snapshots.

The publisher is **installed** and configured via the existing systemd
`atoman-backup.service` `ExecStartPost` drop-in. The installer was executed by
an authorized administrator, its tests passed, and the encrypted snapshot
`20261028T033423Z-ou85era0` was published. The regular timer is enabled.
Observe the next complete scheduled backup cycle to confirm that a newly
verified source dump is also published automatically. A successful NAS pull
verifies ciphertext size and SHA256 and leaves the contents inaccessible
without the separately protected age identity.

For changes to the NAS cron, prefer the TrueNAS middleware API
(`midclt call cronjob.query`, `cronjob.update`) over editing
`/var/spool/cron` directly. The scheduler is configured in the NAS's local
America/Los_Angeles time zone, not the VPS time zone.

## Restoring and monitoring

For recovery, independently download the **ciphertext**, compare its SHA256
to both the VPS receipt and NAS completion marker, decrypt with the offline
age identity on a trusted machine, verify the plaintext tar member hashes and
restore dumps into an isolated PostgreSQL instance. A local restore of
a different source snapshot does not substitute for recovery of a copy
actually fetched from NAS.

The NAS pull utility is at
`AtomanBackups/bin/nas-pull-encrypted-backups.py` and writes only within the
owner-only dataset. It atomically writes `AtomanBackups/pull-status.json`
with `state`, UTC timestamp and newest completed snapshot age, or an explicit
error if the pull fails or the latest complete snapshot is older than 48 hours.
The private `pull.log` preserves transfer history. Neither a schedule being
enabled nor an old `already-verified` result proves the latest backup is fresh.
The status file is a local monitoring aid, not an externally delivered alert:
verify notification configuration separately.

The SFTP read-only account is not an administrator. Its authorized key
is stored outside the chroot at
`/etc/ssh/authorized_keys/atoman_backupread`.
