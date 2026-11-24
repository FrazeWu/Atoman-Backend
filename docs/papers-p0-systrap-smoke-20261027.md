# Papers P0 — standalone gVisor systrap smoke test (2026-10-27)

## Scope

Machine: `vmi3410530` (Debian 13, Linux 6.12, x86_64).

This is a **standalone OCI runtime smoke test**, not a completed LaTeX sandbox certification. It did not change `/etc/docker/daemon.json`, install a global runtime, restart Docker, connect to the production database, or start untrusted TeX code.

## Release provenance

- gVisor version: `runsc version release-20261019.0` (`spec: 1.2.1`).
- Official release source: `https://storage.googleapis.com/gvisor/releases/release/latest/x86_64/`.
- Release archive: `gvisor.tar.zstd`, with accompanying `gvisor.tar.zstd.sha512`.
- SHA-512 verification: `sha512sum -c gvisor.tar.zstd.sha512` reported **OK**.
- SHA-512 digest: `bcbf431b2f9a039f6ab23bfe23986031130c0f149294978cedfd46910901deba0737d8642c848f597ed4b5c72324885db4d75ee86a1f6a90915fbe1f2f2eb915`.
- Runtime and sidecar binaries were extracted to a temporary directory only. The release tar contains `runsc`, `containerd-shim-runsc-v1`, and `gvisor-bin/`.

The OCI bundle was created from a disposable export of the locally available `postgres:16-alpine` image. That export container was deleted before execution.

## Positive smoke result

`runsc --platform=systrap --network=none` successfully ran a process as UID/GID 65534 in the dedicated OCI rootfs.

- Output included `GVISOR_P0_OK` and `uid=65534(nobody)`.
- No `runsc` containers remained after exit.
- Docker's default runtime remained `runc`, and existing Docker/Atoman services stayed active.

## Negative security checks

The container specification used:

- `root.readonly=true`;
- process UID/GID 65534 and `noNewPrivileges=true`;
- empty Linux capability sets;
- a network namespace plus explicit `--network=none`;
- OCI memory limit set to 256 MiB and PID limit set to 32 (configured but **not yet validated under exhaustion**);
- an explicit, size-limited writable `/output` tmpfs.

Observed checks:

| Check | Observed result | Interpretation |
|---|---|---|
| BusyBox wget to 1.1.1.1 | `NETWORK_DENIED` | Basic network isolation smoke passed |
| Read host-only private credential path | `HOST_SECRET_HIDDEN` | Specific host path was not visible; not an exhaustive mount audit |
| Write a world-writable test fixture in rootfs | `ROOTFS_READONLY` with `Read-only file system` | Passed **only with explicit `--overlay2=none`** |
| Write inside /output | `OUTPUT_WRITABLE` | Explicit writable scratch mount works |
| Check underlying exported rootfs after run | No test file present | Host-side rootfs export not modified |

### Security-critical behavior discovered

The first test allowed a write to `/tmp` despite `root.readonly=true`. This is not proof that the underlying rootfs was mutable: gVisor may provide writable tmpfs/overlay mounts. The official filesystem documentation also states that `runsc` defaults to a writable `--overlay2=root:self` layer.

For an immutable Papers compile environment, **do not depend on the OCI readonly flag alone**. The hardened smoke test explicitly set `--overlay2=none`, verified that a world-writable directory on the actual rootfs was not writable, and mounted only `/output` as an explicit writable scratch location.

References:
- https://gvisor.dev/docs/user_guide/quick_start/oci/
- https://gvisor.dev/docs/user_guide/install/
- https://github.com/google/gvisor/blob/master/g3doc/user_guide/filesystem.md

## Remaining P0 blockers

- TeX Live 2025/2026 has not been installed or tested under gVisor.
- No compile tests for NeurIPS/ACL/ICLR, Biber, fonts, or custom `.sty/.cls` have been performed.
- Memory and PID cgroup limits were configured, **not stress-tested**.
- No verification yet of timeout cleanup, cancellation, worker lease recovery, file-handle escape, resource fairness, or long-running stability.
- MinIO ports 9100 and 9101 are still published on all interfaces. UFW allow rules remain; real external access must be checked before deploying private Papers assets.
- PostgreSQL is currently published on 127.0.0.1:5432, but the legacy UFW allow rule for 5432 remains; verify actual exposure rather than assuming it is reachable or unreachable.

**Decision: gVisor systrap passes an initial compatibility and simple isolation smoke test only. P0 is still incomplete; production compilation remains disabled.**

## Runtime cleanup observation

After successful standalone `runsc` exit, the dedicated state directory retained a mounted `null-netns` (`nsfs`) entry even though `runsc list` showed no remaining containers. Removing the test directory returned `Device or resource busy`. The test-owned mount was identified with `findmnt` and explicitly unmounted at its exact dedicated path; the temporary directory was then removed successfully.

This is another reason that P0 cannot be marked complete from a successful smoke run alone. The production Sandbox Launcher must verify all per-task namespaces/mountpoints and use an auditable cleanup and crash-recovery process, rather than assuming `runsc run` exits without leaving host resources.

Final verification: temporary test directory absent; no test-owned containers running; Docker default runtime still `runc`; Atoman API, music worker and Docker service all remained `active`.
