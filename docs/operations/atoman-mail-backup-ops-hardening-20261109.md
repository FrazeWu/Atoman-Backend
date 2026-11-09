# 2026-11-09 Mail backup verification and offsite evidence hardening

This change does **not** alter mail user state, modify a source backup dump, or grant the VPS access to the Mac's recovery private key.

## Verified source and two independent destinations

- `backup-postgres.py` runs a network-isolated, disposable Postgres container. Restrict it to 2 CPUs, 3 GiB RAM, and `maintenance_work_mem=128MB` for a full index-inclusive restore. A failed verification is **not** a completed snapshot; existing completed snapshots remain available. Run the real full restore of a completed backup before declaring this change validated.
- `nas-pull-encrypted-backups.py` retains `get`/`reget` resumability and the complete SHA-256 gate. The large `backup.tar.age` ciphertext may take up to four hours; small metadata remains on its old bounded timeout. Neither a partial file nor an interrupted transfer receives `LOCAL_COMPLETE.json`. Do not overwrite a running `.incoming` NAS pull with an independent transfer.
- The source Backup Health `offsite_transfer_confirmed` always stays false for a **mail-only** offsite recovery; it becomes true only after a separately signed **full website** independent recovery.

## Signed offline witness

`check-backup-health.py` accepts optional `--offsite-evidence-dir`, `--offsite-allowed-signers` and `--offsite-grace-hours` flags. They must be configured as a pair. An offline recovery Mac signs the byte-exact `evidence.json` with its private Ed25519 SSH key using namespace `atoman-mail-recovery` and identity `atoman-mail-attestor`. The VPS stores only the public `allowed_signers` entry and the signed evidence. The health service verifies the SSH signature and matches the record against the **latest** completed source ID, source manifest SHA-256, capsule SHA-256, and full-site cipher SHA-256. A verified mail-only attestation yields:

- `mail_capsule_offsite_recovery_confirmed=true`
- `offsite_evidence_status=verified_mail_only`
- `full_site_offsite_recovery_confirmed=false` and `offsite_transfer_confirmed=false`

Only signed `full_restore_verified=true` with a full-site report SHA and matching source/cipher hashes yields the separate full-site true flags. A forged signature, symlink evidence, wrong hash, or malformed attestation makes health `error` and triggers the existing failure alert. A **newer completed backup** is initially `pending` while its own offline verification is running; signed evidence for an older backup cannot mark it successful. After the configured grace period (default 36 hours), missing current evidence makes health `error`.

Never copy the private signing key to the VPS, NAS or into a Git repository. Save it only in a Mac private directory. To revoke the attestor, remove its public entry and the current evidence and rotate the Mac private key.

## Disposable internal mail test

`mail-internal-disposable-acceptance.py` requires `--confirm-disposable-only` and a new private `--receipt`. It creates two unique Stalwart accounts in the existing authorized Atoman domain, uses a fresh random password for each, sends **only** a single synthetic message in each direction via SMTPS 465 and STARTTLS 587, and receives via IMAPS 993. It asserts the TLS certificate, unique subject marker, from/to, MIME body and attachment SHA-256, then destroys **exactly** the two accounts it created. No real user mailbox, external recipient, or Atoman site credential is touched. On an incomplete cleanup the private receipt contains exact generated account IDs for operator follow-up. No secret is printed or persisted.

## Operational distinctions

- Mail-only NAS restoration passing is **not** proof of the complete site's NAS restoration. Monitor those fields separately.
- A `data-and-mail` backup profile restores table data but does not rebuild all application/development secondary indexes. A true full-index verification must be independently run and distinguished in status.
- The offsite health script is deliberately not a replacement for the offline restoration application. It verifies a signed receipt from that independent application, and does not decrypt on the VPS.
- Retain previous verified backups until a newer full-site offline restore is complete. Repeated full-archive transfer failures must not erase the previous recovery baseline.
