# Atoman Papers — P0 非 root Worker / root Launcher 权限隔离验收

日期：2026-10-28；机器：`vmi3410530`；工作分支：`feat/papers-unix-launcher-20261028`；定位：**单机 P0 验证，不是生产服务**。

## 设计目的

旧版 P0 Worker 以 root 运行，并同时访问 PostgreSQL、项目快照、TeX Live 离线归档、systemd、gVisor 和宿主文件系统。现将其拆为两个**独立 Unix 进程**，以严格受限的本机 Unix Socket 作为唯一执行桥：

```text
  PostgreSQL (测试容器 127.0.0.1:随机端口)
         |                              ^
         | 编译队列 DML                  | 只读 SELECT，最小授权
         v                              |
  Worker (UID/GID 65534, non-root)       |
         |                              |
         | 固定 JSON 请求: run / reap     |
         | 仅 Job+Token+Epoch+SandboxID  |
         v                              |
  root:65534 Unix Socket (0660)          |
         |  Linux SO_PEERCRED            |
         v                              |
  root Launcher ------ ResolveLease -----+
         |
         | 不信任客户端传来的源文件/镜像/执行路径
         | 从数据库重建可信 Claim
         v
  固定 TeX 镜像 + SHA-256 源快照/离线归档校验
         |
         v
  内部 OCI Bundle + 受控 gVisor Supervisor
         |
         +-----> 真实 pdfLaTeX / PDF 输出
         +-----> runsc + systemd scope + netns 清理
```

### 重要安全边界

1. **Worker 不再具有 root 权限。** 独立进程以 Linux UID/GID 65534 运行；运行期间 `ps -eo uid,gid` 确认确实为 65534。它只读取自己的 PostgreSQL 编译队列/租约，并发送受控的 `run` 和 `reap` 请求，不接触归档或 rootfs。
2. **Unix Socket 仅允许单一受控身份。** Socket 文件属主 `root:65534`、模式 `0660`；其父目录 `0710`，无其他用户访问权限。服务端使用内核 `SO_PEERCRED` 同时验证 UID/GID，拒绝 root 伪装为普通 Worker。客户端验证对端 UID=0，防止连接到普通用户伪造的 Socket。
3. **不接受通用宿主命令。** 协议只有 `run/reap`，没有 shell、Docker、镜像选择、自由挂载目录、脚本参数或任意可执行文件输入。请求长度 16KiB 上限，不认识的顶层字段和操作会被拒绝。
4. **特权端再校验当前数据库租约。** `papersqueue.ResolveLease` 以真实 PostgreSQL 数据重建项目 UUID、不可变源快照和环境 Digest；验证全局单槽位、token、epoch、worker 身份、当前时间、状态和权限。过期、撤销、被新 epoch 覆盖的任务不能启动。执行中每隔 2 秒复核；Worker Socket 断开或租约失效时取消执行，并由 Supervisor 执行独立清理。
5. **数据库账户分离。** root Launcher 使用 P0 一次性 PostgreSQL 角色 `papers_launcher_ro`，只有四张指定表的 `SELECT` 权限；角色 `default_transaction_read_only=on`。启动时验证 `current_user`、只读配置和无 `UPDATE` 权限，若不符直接拒绝启动。Launcher 进程**不接收**可写的 `PAPERS_P0_TEST_DSN` 环境变量；普通 Worker **不接收**特权 Launcher 的只读凭证。
6. **内存与容器隔离与原有 P0 策略一致。** OCI rootfs 只读、无网络、无 Linux capabilities、`noNewPrivileges`、固定资源限额和 TeX `-no-shell-escape`。真实执行与清理结果进入持久数据库状态，不能由客户端假造“cleanup verified”。
7. **系统恢复优先。** Worker 收到 SIGKILL 后，root Launcher 会因 Unix Socket EOF 尝试取消并清理其正在执行的任务；如果根进程异常、清理未获证实，后续 Worker 仍必须经租约超时、`recovering`、原 SandboxID 资源回收，再执行新任务，数据库遇到不可验证的清理会阻断后续任务。

## 本轮新增文件

- `internal/platform/papersqueue/launcher_lease.go`、`launcher_lease_test.go`：根服务独立二次租约验证；忽略调用方伪造的项目/环境字段，只使用数据库中的可信内容。
- `internal/platform/paperslauncher/server_linux.go`：Linux Unix Socket root 服务、`SO_PEERCRED`、只允许非 root 的一个固定 Worker 身份、单槽位执行、输入限额、断开取消、租约定期核查。
- `internal/platform/paperslauncher/client_linux.go`：非 root Worker 的 `papersqueue.SandboxExecutor` 客户端实现，使用 root 身份认证与固定响应格式。
- `internal/platform/paperslauncher/server_linux_test.go`：**真实操作系统进程**的 UID/GID 降权、Root 客户端拒绝、协议白名单、对端断开取消和不可任意扩展命令测试。
- `cmd/papers_p0_unix/main.go`：明确限制到随机回环测试 PostgreSQL 的 P0 CLI；分别以 root/UID65534 运行测试服务和 Worker；不能连接生产数据库。
- `scripts/papers/p0-unix-launcher-e2e.sh`：一次性 PostgreSQL、独立测试只读角色、严格文件权限、固定 gVisor/TeX Live、真实 Worker SIGKILL、租约失效、reap、再次编译及完整清理。

## 已通过的实机验证

**正常路径**（初版原型，执行成功）：
- `P0_UNIX_SOCKET_AND_ASSET_ACL_VERIFIED`
- Worker 进程 `UID=65534/GID=65534`，root Launcher 单独运行。
- `P0_UNPRIVILEGED_WORKER_STEP_PASSED uid=65534`
- `pdflatex` 在真实 gVisor 内成功编译 1 页 PDF，31,860 字节。
- PostgreSQL 任务 `succeeded`，`attempts=1`，`epoch=1`。
- `P0_REAL_TEX_BASELINE_SUCCEEDED`；测试 Scope、容器和工作目录清理完成。

**真实 SIGKILL + 只读数据库账户路径**（加强只读角色后的最终验收，执行成功）：

```text
P0_DURABLE_SEEDED ...
P0_UNIX_SOCKET_AND_ASSET_ACL_VERIFIED
P0_REAL_SENTRY_ACTIVE_BEFORE_KILL=pc-5ee80e79d5d94099be2bcf40
P0_FIRST_WORKER_SIGKILL_CONFIRMED
status=running attempts=1 epoch=1
P0_OLD_LEASE_EXPIRED_BEFORE_REAPER
P0_REAL_ORPHAN_SANDBOX_REAPED
status=queued attempts=1 epoch=2
P0_RETRY_REAL_PDFLATEX_FINISHED
P0_REAL_GVISOR_WORKER_KILLED_RECOVERED_AND_LATEX_SUCCEEDED
P0_ROOT_LAUNCHER_UNPRIVILEGED_WORKER_REAL_RECOVERY_PASS
P0 disposable scopes, containers and workspace verified cleaned.
```

单次故障恢复中，被终止的是**确切的非 root Go Worker PID**，而不是 Shell 包装器；根 Launcher 持续工作。新 Worker 不被允许跳过旧沙箱回收，最终同一快照再次真实编译，任务在第二次尝试成功、epoch=3。进程退出码为 0。

## 必须保持 No-Go 的项目

1. 当前 UID/GID 65534 是为了可重复验证而使用的系统 `nobody`，**不可用于生产**。需要为编译 Worker 和 Launcher 分别规划专用固定系统账户、socket ACL 和 SELinux/AppArmor 约束。
2. root Launcher 仍在 P0 测试脚本中手工启动，**尚未创建、安装或验证生产 systemd unit/socket**。需要 systemd 的能力白名单、`NoNewPrivileges`、文件系统写入白名单、资源约束、启动重启和退出清理策略；不得直接复用测试 root 命令。
3. `papers_launcher_ro` 仅存在于一次性 PostgreSQL 测试容器。生产最小权限数据库角色、Secret 管理、连接审计和凭证轮换仍未实施。Worker 仍使用 P0 可写测试角色，不能复用于生产。
4. 特权 Launcher 仍持有本地完整 TeX Live 归档的文件访问能力；生产需要可信、不变的私有对象存储与环境 Registry、数据集/文件权限、签名或校验、可审计的发布操作，避免从用户请求直接指定路径。
5. 当前仅有 `Runner.Step` 单次执行，尚未接入可管理的 daemonized Worker 调度服务、持久度量、告警、可恢复的磁盘配额管理和产物分发。
6. 需要更大范围的失败注入：**Launcher 自身强制退出**、数据库网络中断、DB 权限轮换、旧版本 rootfs 恢复、磁盘耗尽、恶意归档 symlink、输出限制与日志截断。
7. 已验证的是隔离环境中的 **P0 功能/安全子项**，不代表整个 Atoman Papers 可开放给用户。没有部署 production API；不更改 Docker 默认运行时；不推送 `main` 或自动上线。

**结论：** Unix Socket 最小权限边界 + 真正非 root Worker + 独立只读 root Launcher 数据库角色 + 真实 gVisor PDFLaTeX + 故障恢复已集成验证。**Atoman Papers 生产仍为 No-Go。**
