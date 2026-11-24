# Atoman Papers — P0 Sandbox Launcher 原型审计（2026-10-28）

**范围：** `vmi3410530`，后端独立 worktree `feat/papers-sandbox-launcher-20261028`。尚未合并 `main`、安装 gVisor 到系统级目录、修改生产 Docker 或开放 Papers 编译 API。

## 1. 新增的 Go 控制平面

新增 `internal/platform/paperssandbox/`，目前仅为 P0 原型和测试基础：

- `status.go`：统一任务结果，严格区分编译失败、运行时异常、cgroup 限制触发、超时、取消、产物缺失、隔离验证失败与清理失败。
- `telemetry.go`：解析**宿主机侧可信** systemd/gVisor Scope 日志，保留 `pids.events`、`memory.events`、`cpu.stat` 的最大观测计数。不得将用户 LaTeX 编译输出混入该输入，以免伪造 SENTRY 行。
- `supervisor.go`：单进程一次仅运行一个高资源任务；每个任务无论成功失败都要调用独立清理流程；清理不可靠立即隔离（quarantine），拒绝后续任务；`RecoverOrphans` 为未来持久任务队列提供失效任务回收接口。
- `oci_policy.go`：仅接受窄化的、平台预先生成的 OCI 配置；要求非 root、只读根目录、禁止 Capabilities/OCI Hooks、隔离网络与 PID/mount namespace、精确 cgroup ID/限制值、固定 TeX 命令、明确禁用 shell escape、仅允许批准的输出挂载和定额 tmpfs。
- `scope_backend.go`：P0 受限执行适配器，使用 systemd transient Scope 调用已有受控 helper 并解析证据；支持对确切沙箱 ID 执行 `runsc list/kill/delete`，检查本次专属 `null-netns`，核查对应 Sentry/Wrapper Scope 是否仍在运行。拒绝已经存在的 PDF，防止旧产物伪装成新编译成功。

**重要：** 这些接口没有注册到 HTTP，也没有允许用户提供任意 Shell、Dockerfile、OCI 配置或系统二进制路径。当前构造 OCI Bundle 的受控生产环境管理器尚未实现；`ScopeBackend` 只能消费平台生成并验证的 Bundle。

## 2. 失败状态语义

| 核实信号 | 结果 | 调度规则 |
|---|---|---|
| 验证过的 Scope + 运行成功 + 新 PDF 有效 + 清理成功 | `completed` | 后续任务可继续 |
| exit 2 + `pids.events.max > 0` | `resource_exhausted_pids` | 仅在清理完整后允许下一任务 |
| 内核 OOM 事件 | `resource_exhausted_memory` | 仅在清理完整后允许下一任务 |
| PID、OOM 事件同时出现 | `resource_exhausted_ambiguous` | 不武断归因，必须审计 |
| Deadline 到达 | `timed_out` | 必须显式回收并检查残留 |
| 用户取消 | `canceled` | 不得跳过清理 |
| 没有可信 Sentry 或资源数值不匹配 | `isolation_failed` | 不允许标记完成 |
| 运行成功但没有本任务新 PDF | `artifact_missing` | 不允许标记完成 |
| 清理未能验证 | `cleanup_failed` | **隔离整个本进程调度器，不再启动任务** |

只看运行时退出码不可靠：之前的真实 P0 PID 测试已记录 `pids.events.max=1` 与 `runsc exit=2` 同时发生。分类必须优先使用可信内核观测，而不能将它当作一般 TeX 编译错误。

## 3. 已通过的测试范围

使用 Go `testing` + `-race` 测试：

- PID 失败后清理成功，下一项模拟编译正常执行；
- 清理不完整，Supervisor 自动隔离并阻止下一任务；
- 同进程拒绝并发高资源任务；
- 取消/超时期间依然执行清理；
- 启动恢复流程可回收此前遗留任务，失败时隔离；
- 可信 Scope 日志解析出 PID、OOM、CPU 节流；
- 资源值不一致、cgroup ID 错误、无 PDF、上一次旧 PDF、超大/损坏日志均拒绝成功；
- 非 root/只读根文件系统、权限、参数、OCI Hooks、宿主命名空间及挂载白名单的拒绝测试；
- 使用模拟 `systemd-run`/`runsc` 命令执行器验证真实适配器的指令顺序与回收决策；
- symlink 工作区逃逸路径拒绝。

这批测试**使用模拟的操作系统命令，不是实机 gVisor Go 适配器验收**。之前 `scripts/papers/p0-gvisor-texlive-smoke.sh` 已独立完成过真实 TeX Live 2026/gVisor 运行和资源测试，详细记录见 `papers-p0-texlive-cgroup-20261027.md`。两组证据不能相互替代。

验证命令：

```sh
cd /root/Atoman/.worktrees/papers-sandbox-launcher-20261028-backend
go test -race ./internal/platform/paperssandbox -count=1
go vet ./internal/platform/paperssandbox
go build ./...
```

## 4. 尚未完成的生产级事项

1. 先实现可信 OCI Bundle/环境构建与签名验证，避免用户影响宿主机命令参数、配置文件、路径或临时工作目录。
2. 通过独立的最小权限 Sandbox Launcher 服务运行真实 gVisor，完成端到端的 PID 异常退出→显式清理→下一次实际 LaTeX 编译，并进行真实取消、崩溃和资源限额测试。
3. 将持久任务队列（PostgreSQL）、单实例/跨实例租约、fencing token、任务结果和环境 digest 与 Supervisor 接通。当前互斥锁**只针对单进程有效**，不能替代全局队列。
4. 持久化和轮转 Scope 事件与任务结果；在 Worker 异常退出后依据租约回收孤儿任务，并验证**跨重启**恢复。
5. 明确 gVisor 的隔离与 mount/netns 清理职责；不赋予业务 API Docker Socket、root 权限或直接运行沙箱的能力。
6. 验证 TeX Live 2025、XeLaTeX、Biber、NeurIPS/ACL/ICLR 学术模板和依赖/辅助工具白名单。
7. 完成 MinIO 网络暴露、现有服务权限与资源熔断等 P0 安全阻断项。

**P0 发布结论：No-Go。** 完成了可信事件归因与受限执行原型的代码和模拟验证，尚不具备真实用户上传 LaTeX 后安全在线编译的生产条件。

## 2026-10-28 后续实机验证

已使用新增的 Go `ScopeBackend` + `Supervisor` 完成固定环境下的 **真实 PID 超限 → cgroup 事件归因 → 清理 → 后续真实 LaTeX 编译成功**，不再仅限模拟命令层。测试中 PID 任务 `runsc exit=2`，`pids.events.max=1`，归因为 `resource_exhausted_pids`；随后的 PDFLaTeX 任务 `completed`，两个任务的清理均获验证。详见 [真实 Launcher 恢复验收](papers-p0-live-gvisor-recovery-20261028.md)。生产级队列、独立权限与综合安全验收仍缺失，总体 **No-Go**。
