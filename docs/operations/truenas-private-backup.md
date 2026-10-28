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
- Actual independent age decryption from the NAS copy is a distinct check:
  a long direct stream was interrupted by a remote SSH disconnect; subsequent
  verification may use resumable transfer of the *encrypted* file to Mac.
  Never equate a truncated stream with a corrupted stored ciphertext.
- A TrueNAS middleware cron entry has been created as job ID `2`,
  **currently disabled**, under `fa`, configured for 02:30 local
  TrueNAS time (`America/Los_Angeles`), and using `flock`.
  It does not delete backups. Do not mark recurring backup as complete until
  the VPS automatic publisher has been installed and an end-to-end scheduled
  cycle passes.

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

The source is kept on the isolated task branch. A reviewed one-time installer
is at `/root/Atoman/tasks/install-atoman-nas-offsite.sh`. It installs the
publisher and adds a systemd `ExecStartPost` drop-in for the existing
PostgreSQL backup service. The installer's planned execution was not completed
via remote tooling; it must not be described as already installed.

After verified publisher installation, the NAS cron entry `2` may be
enabled through TrueNAS middleware and validated by an on-demand pull.
A successful NAS pull verifies the ciphertext and leaves the recovered
content inaccessible without the separately protected age identity.

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
`AtomanBackups/bin/nas-pull-encrypted-backups.py` and only writes inside
the owner-only dataset. Failures must remain visible through the private
`pull.log` and system task status; do not assume that an enabled schedule
alone proves successful offsite copies.

The SFTP read-only account is not an administrator. Its authorized key
is stored outside the chroot at
`/etc/ssh/authorized_keys/atoman_backupread`.
