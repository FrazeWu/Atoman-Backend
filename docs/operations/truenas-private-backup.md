# Atoman offsite backup to the private TrueNAS dataset

## Scope and state

Destination: TrueNAS `172.16.10.201`, POSIX dataset
`/mnt/basic_pool/AtomanBackups`, owned by `fa`, permissions 0700,
no SMB share and no access for group/others. Dataset creation and permissions
were reported by the operator but remain **unverified via SSH**.

At the initial connection test, SSH from FAMBP.local to TrueNAS was reachable,
but both default SSH keys and the new dedicated setup key were rejected for
`fa`. The VPS cannot route directly to this private address. **Do not install
a periodic pull task until authorized NAS access and the real directory ACL
have been verified.**

### One-time NAS setup authorization

In TrueNAS, append the following **public** SSH key to user `fa` using
Credentials → Users → fa → Edit → Public SSH Key; preserve existing keys:

```text
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIF2G2Fi5KJRp+vbK2aQ2VIFEpPaz1pnpcPUnJWCp6eRZ atoman-nas-setup-20261028
```

The matching setup private key is on FAMBP.local at
`/Users/fafa/.ssh/atoman_nas_setup_20261028`, mode 0600. Do not copy it to
the VPS or put it into Git. After authorization, independently confirm
`id -un`, the real dataset's owner/group/permissions and free space.
Avoid any recursive ownership or ACL operation on other datasets.

## Backup encryption

An age recovery identity was generated on FAMBP.local, *not* the VPS:

```text
/Users/fafa/.config/atoman-backup/age-recovery-identity.txt
```

The corresponding public age recipient is:

```text
age1rfgu75985jpk7aml06uvdzm0ewa4p7j9ua7l8vut3ek5eueddgfs542fe8
```

Only the public recipient is needed on VPS. This key's decryption ability
has not yet been independently verified on Mac through a real cross-host
restore. The Mac had FileVault disabled when checked; **do not regard the
single mode-0600 file as an adequate independent recovery-key archive**.
The owner must keep a second protected offline copy and should enable
FileVault or move the identity to a protected key store. Never send the
private identity through chat.

A restored-and-verified October 27 PostgreSQL snapshot has been encrypted on
VPS under `/var/backups/atoman/encrypted-export-staging/`. The result has
an age ciphertext and SHA256 receipt; it is currently **not offsite**.
The October 28 scheduled database backup was concurrently performing
its isolated restore check during setup.

### Transfer model

The secure intended model is **NAS-initiated pull**, without depending on
Mac uptime:

1. A VPS systemd task encrypts only `COMPLETE` + independently
   restore-verified PostgreSQL snapshots, using the public age recipient.
2. A dedicated non-root SSH user on VPS exposes only a **read-only SFTP**
   area containing ciphertext and nonsecret receipts. No shell, password
   authentication, write, port forwarding, agent forwarding or PTY.
3. TrueNAS `fa` runs `nas-pull-encrypted-backups.py` from a private
   directory, with an independent SSH key restricted to the read-only VPS
   SFTP user and pinned SSH host key.
4. Every fetched ciphertext must match the source receipt's exact size
   and SHA256 before an atomic local completion marker is installed.
5. The NAS stores ciphertext; the age private recovery key remains off
   VPS and outside the normal NAS pull workflow.

Never use the general root SSH key for the recurring pull. The setup SSH key
and the age recovery key serve different purposes.

### Activation requirements and verification

Before claiming the backup operational, complete all of:

- TrueNAS user `fa` has verified SSH access and effective 0700 dataset mode.
- The restricted VPS SFTP account and NAS pull SSH identity are installed;
  their scope is checked against arbitrary read/write and shell attempts.
- A full production ciphertext is transferred and matches SHA256 on NAS.
- A second independent machine decrypts a fetched copy with the offline
  recovery identity and verifies the archive and database restoration.
- A TrueNAS scheduler runs the pull as `fa`, checks exit status and notifies
  on failures. A second run is idempotent; connection failure leaves the
  existing verified NAS snapshots unchanged.
- Separate retention policies are explicitly approved; the current
  nas-pull script **never deletes snapshots**.

The pull script rejects unexpected snapshot names, invalid receipt names,
untrusted SSH host keys, weak directory permissions and mismatched
transferred data. The `recovery_verified` field always remains false
until a separate actual restoration has succeeded.

## Scope not yet covered

PostgreSQL logical dumps and globals are included. Production R2 objects,
external media files and application secrets are *not* covered by the
PostgreSQL tar alone and need their own explicitly scoped offsite policy.
The absence of a remote copy or an unrecoverable key must produce an alert,
not a success message.
