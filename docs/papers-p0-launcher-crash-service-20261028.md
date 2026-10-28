# Atoman Papers — P0 root Launcher crash/restart 与 systemd 服务部署边界

日期：2026-10-28。工作分支：`feat/papers-launcher-crash-service-20261028`。
实测机器：`vmi3410530`，现有 Atoman 后端、音乐 Worker 和 Docker 未修改。

**总体发布结论：P0 No-Go。** 本分支并未安装/启用生产 Launcher、未创建宿主机专用用户、未提供可运行的生产 Worker 常驻程序、未连接正式对象存储、未合并或推送 main。

## 1. 新发现及修正的安全边界

以前 Worker 对 Unix Socket 出错时返回无清理证明的空结果，并调用队列的 Complete/FinishRecovery。此路径会把 **“Launcher 进程已死、还不知旧沙箱是否存在”** 误当作 **“Launcher 明确核实清理失败”**，从而错误地永久阻断全局槽位。

### 改造后的双路径

- **执行结果未知（`ErrExecutionUncertain`）**：Unix Socket 连接失败、无响应、响应畸形、未获有效操作回执，或者特权 Launcher 失联。Runner **不调用** `Complete` 或 `FinishRecovery`，不自认清理成功/失败；保留 `running` 或 `recovering` 以及旧的 Sandbox ID、fencing token/epoch。心跳停止后，仅在 PostgreSQL 租约过期时，新 Worker 才能领取 `recovering`。
- **真正获得的清理失败证明（`CleanupVerified=false` / `CleanupFailed`）**：`Complete` 继续持久阻断全局槽位（`blocked=true`）。主动检查出残存的 gVisor/Scope 不会自动跳过；必须走人工审计和安全处置。
- 无论何种情况，任何旧租约持有者都不能写回当前成功状态，不能替新 Worker 释放槽位。
- 如果恢复阶段的 Launcher 也被中断，仍保持 `recovering`，只有新一轮租约过期后才能再次回收同一个旧 Sandbox ID。不会凭客户端断开而视为成功。

### Unix Socket 重启接管

新增 `ListenRecoverStale`：
- 保留原 `Listen` 的“已存在即拒绝”严格语义；
- 只有 root 在 `root:<WorkerGID>`、权限 `0710` 的专用目录内，才可接管一个**根用户拥有的、权限 0660、组为指定 Worker GID** 的 Unix Socket；
- 内核 `connect` 必须确认旧 Socket 已无人监听（`ECONNREFUSED`）；仍能连接、权限不合规、普通文件、符号链接、不安全父目录均拒绝；
- 删除前重查设备号、inode 和文件模式，避免错误路径误删（该目录仅允许 root 创建项）；重新建立 Socket 后仍验证 `SO_PEERCRED` 和数据库租约；
- 不清理用户端文件，不支持通用 `unlink` 或 TCP 监听。P0 CLI 专用 `server` 模式已接入该功能。

## 2. 真正的 root Launcher 崩溃恢复实测

一次性 PostgreSQL 16 / TeX Live 2026 镜像 Digest / 官方 SHA-512 验证的 gVisor `release-20261019.0`，非 root Worker UID/GID 65534、root Launcher 只读 DB `papers_launcher_ro`，专用 `127.0.0.1` 随机端口和私有工作区。

```text
P0_DURABLE_SEEDED ...
P0_UNIX_SOCKET_AND_ASSET_ACL_VERIFIED
P0_REAL_SENTRY_ACTIVE_BEFORE_KILL=pc-50ee430148f44002bfb5992c
P0_REAL_LAUNCHER_PROCESS_CONFIRMED=172695
P0_ROOT_LAUNCHER_SIGKILL_CONFIRMED
P0_LAUNCHER_TRANSPORT_UNKNOWN_PRESERVES_OLD_LEASE
P0_ROOT_LAUNCHER_RECOVERED_OWNED_STALE_UNIX_SOCKET
status=running attempts=1 sandbox=pc-50ee430148f44002bfb5992c epoch=1 blocked=false
P0_OLD_LEASE_EXPIRED_BEFORE_REAPER
P0_ROOT_LAUNCHER_RESTART_REAPED_PREVIOUS_SANDBOX
status=queued attempts=1 sandbox= epoch=2 blocked=false
P0_RETRY_REAL_PDFLATEX_FINISHED
P0_REAL_GVISOR_WORKER_KILLED_RECOVERED_AND_LATEX_SUCCEEDED
P0_REAL_ROOT_LAUNCHER_SIGKILL_STALE_SOCKET_RESTART_RECOVERY_PASS
P0 disposable scopes, containers and workspace verified cleaned.
```

**实际动作**：测试程序先确认 root Launcher Go 二进制真实 PID（`comm=p0-unix`）、真实 gVisor Sentry Scope active 与 DB 任务 running，再向该 PID 发送 SIGKILL。等待 Worker 因 Socket EOF 退出，检查 `ErrExecutionUncertain` 与数据库状态没有被错误释放。对原始遗留 Socket 重启新 root Launcher，等租约过期后让新非 root Worker 执行精准 Reap，再重新领取新 Sandbox ID，完成真实 PDFLaTeX。最终队列校验要求 `status=succeeded`、`attempts=2`、`epoch=3`、槽位空闲且非 blocked。全程没有对生产服务、主数据库或 Docker 默认运行时做修改。

**测试结果**：P0 独立实机 E2E 退出码 0。此项与上一轮 “SIGKILL 普通 Worker、root Launcher 保持在线” 是两个不同的故障注入场景。

## 3. 拟议生产账户与 systemd profile（尚未安装）

本次添加了 `deploy/papers/systemd/` 下两个 **明确 NO-GO / 不能直接安装** 的 unit 配置参考。配置包含:
- 普通 Worker 使用专用静态身份 `atoman-papers-worker`，无 sudo、Docker group、home 或交互 shell；强制关闭 capability；限制设备、命名空间、内核控制、内存/任务数；可访问的网络仅限回环，正式 DB 连接路线另行审核。
- root Launcher 单独运行、只读数据库角色、固定目录 `/var/lib/atoman-papers`、Unix Socket 位于 `/run/atoman-papers/uds/launcher.sock`，ACL `root:atoman-papers-worker 0660`、目录 `0710`，以 `ProtectSystem=strict` 限制文件系统写入。gVisor/cgroup 所需能力边界**仍须现场权限审计**，目前不能宣称 root 的 capability 已得到实际缩减。
- 两个 unit 均设置不存在的显式安全审批条件 `ConditionPathExists=/etc/atoman/papers/SECURITY_REVIEW_APPROVED` 且 `ExecStart=/usr/bin/false`，确保即使被误安装或启用，也不会启动任何编译服务。
- `scripts/papers/check-papers-systemd-profiles.sh --check` 只做静态 `systemd-analyze verify` 和关键保护开关审查。系统中已有的 1Panel unit `StartLimitIntervalSec` 位置警告与本次 unit 无关。
- `scripts/papers/provision-papers-identities.sh --plan` **只读输出**拟议账户与 ACL，并检测不应属于 Docker group；不会修改 `/etc/passwd`，也没有 `--apply` 命令。

### 本次不在生产服务器创建系统身份的理由

当前 **没有生产级 Worker 长期轮询服务和独立的可执行 Launcher**：本机 P0 CLI 强制使用一次性随机本地测试数据库与固定测试 UID/GID；systemd 服务即使创建了账户也无法正确上线。提前安装永久系统账户/服务会让尚未经过 gVisor Capability Review 的 root 进程获得多余攻击面。所以本轮只提供不会生效的最小权限部署模板，待生产二进制、环境 Registry 和长期运行监督完成后再按审批流程落地。

## 4. 代码与测试

- `internal/platform/papersqueue/runner.go`：区分 `ErrExecutionUncertain` 和真实清理失败；
- `internal/platform/papersqueue/runner_uncertain_test.go`：真实 PostgreSQL 中验证未知状态留下租约、租约超时后清理并重试；恢复期间 Launcher 再次失联不能视为清理成功；真正清理失败继续阻断；
- `internal/platform/paperslauncher/client_linux.go`：断线、超时、未收到回执、协议错误等转换为不确定结果；保留明确的清理失败回执；
- `internal/platform/paperslauncher/recovery_linux.go` / `recovery_linux_test.go`：旧 Unix Socket 安全接管和拒绝测试；
- `cmd/papers_p0_unix/main.go`：root Launcher 使用上述接管入口，不改变非 root Worker 协议；
- `scripts/papers/p0-launcher-sigkill-restart-e2e.sh`：只作用于一次性资源的真实 root Launcher SIGKILL 恢复场景；
- `deploy/papers/systemd/*.service`、`scripts/papers/check-papers-systemd-profiles.sh`、`scripts/papers/provision-papers-identities.sh`：不可直接上线的部署安全参考、只读检查与账户规划。

## 5. 上线仍未满足（NO-GO）

1. 真正的静态系统用户/组及用户权限创建与回滚；验证 `User=` 运行身份、专属目录权限、启动/重启/清理服务生命周期；
2. 编译 Worker 后台运行循环、健康检查、抢占与全局环境构建串行配额；不能继续用 P0 单次 `Runner.Step`；
3. 独立 Launcher 生产二进制和可靠 Secret 加载，限定最低 root capability；systemd `ExecStart=/usr/bin/false` 不得在未安全审查前替换；
4. 生产签名或不可变 Registry、私有对象存储、归档大小/路径处理、产物分发、运行时监控与审计；
5. 真实 systemd unit 的在线启动、daemon reload 与 watchdog、Launcher/DB 联动崩溃、资源耗尽和恶意 TeX 实验；
6. 目前 P0 使用的 `nobody=65534` 是**测试专用身份**，绝不可直接作为生产用户复用。

**结论**：root Launcher 崩溃后的 fencing 租约恢复，以及严守旧 Socket 所有权的安全接管均已实机通过；生产账号和 systemd 配置仍停留于 **可检查、不可误启动的工程草案**。无生产上线变更。
