# Atoman Papers — P0 真实 Worker 强制终止与持久队列恢复验收

日期：2026-10-28；机器：`vmi3410530`；开发分支：`feat/papers-durable-worker-20261028`。

> **发布状态：总体 P0 No-Go。** 本次仅验证特定、一次性隔离环境中的真实恢复子项。没有启用生产 Papers 编译 API，没有注册后台生产 Worker，没有把 root/sandbox 权限授予当前业务服务。不要把 P0 本机测试适配器投入生产。

## 一、目的和真实运行时

之前已经独立验证了 gVisor 的 PID 超限诊断（`runsc exit=2`、`pids.events.max>0`）与 PostgreSQL 的跨进程租约隔离。本轮首次将 **PostgreSQL 真正的持久队列 → 不可变源快照/离线环境验证 → OCI 沙箱 → gVisor PDFLaTeX → 进程崩溃后回收与重试** 组成同一条实机执行链。

固定环境：

- gVisor `runsc release-20261019.0`（systrap）；官方下载包两次通过 SHA-512 校验：
  `bcbf431b2f9a039f6ab23bfe23986031130c0f149294978cedfd46910901deba0737d8642c848f597ed4b5c72324885db4d75ee86a1f6a90915fbe1f2f2eb915`。
- 离线 TeX Live 2026 镜像固定 Digest：
  `texlive/texlive@sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4`。
- 使用专门新建、绑定本机 `127.0.0.1` 随机端口的 PostgreSQL 16 Alpine `tmpfs` 测试容器；严格拒绝任何生产 DSN / 默认 5432 端口。
- `paper_compile_slots(id=1)`：单全局槽位 + 独立令牌 + 单调递增 fencing epoch。租约 12 秒，Worker 每 4 秒心跳；崩溃后先领取 `recovering`，再清理旧 Sandbox ID。
- 受限 OCI：guest UID/GID 65534、capabilities 为空、只读 rootfs、禁用网络和 shell escape、Sentry cgroup 限额可观测、受限 `/output`；PDFLaTeX 使用固定参数。P0 测试在 root 权限的独立维护进程启动，非生产最小权限 Launcher。
- 源快照：包含固定的单个 `main.tex`、项目 UUID 和 revision=2。序列化 JSON 做 SHA-256 内容寻址。实际归档按 TeX 版本与镜像 Digest 锁定，再用档案 SHA-256 验证后展开到新任务的只读 OCI rootfs。

## 二、测试修复与防止虚假验收

本次迭代发现并处理以下重要测试缺陷：

1. **曾误杀 Shell 而非 Go Worker：** 测试脚本使用 `run_step ... &` 后，`$!` 为 zsh 后台函数对应的 Shell，而真正的 Go Worker 子进程还在编译。因此早期所谓 SIGKILL 确认不能作为真实 Worker 崩溃证据。现在直接后台启动 `p0-durable` 二进制，并在 `SIGKILL` 后执行 `wait` 检查退出码 137；失败前不允许打印「恢复成功」标记。
2. **编译时间过短，竞争窗口：** 原测试 `main.tex` 约 3 秒完成，可能在观察 Sentry 后立即结束。现使用经过验证的、有限次 TeX 计数循环；杀进程前读取 DB 确认任务仍是 `running`，之后禁止看到 Worker 自己打印完成标记。
3. **目录过严使沙箱无法执行 pdfLaTeX：** 新的 P0 Go Stager 先前在 `umask 077` 下创建的宿主机路径不可穿越，导致 gVisor 报 `permission denied` 找不到 `pdflatex`。现采用私有父目录、仅 GID 65534 的路径执行权限（0710）和只读 rootfs 下必需的 0755/0644 权限，并在 Go 单测中验证。
4. **Sandbox ID 截断在连字符：** 原 `uuid[:24]` 生成的 ID 可能以 `-` 结尾，现改为 `pc-` 加 **24 个十六进制字符**，用于确切的 systemd 与 gVisor 标识。
5. **租约不允许凭主观等待判定过期：** 现在直接查询一次性 PostgreSQL 的 `lease_expires_at < CURRENT_TIMESTAMP`，确认过期后才允许恢复 Worker 上场。

## 三、真实测试成功记录

一次真实的、已经通过的测试序列：

```text
P0_REAL_SENTRY_ACTIVE_BEFORE_KILL=pc-be32acd6e0aa4823a60de705
P0_FIRST_WORKER_SIGKILL_CONFIRMED
job=164155a4-8739-43e3-87b3-6739ae1891c9 status=running attempts=1 ... epoch=1 blocked=false
P0_OLD_LEASE_EXPIRED_BEFORE_REAPER
P0_REAL_ORPHAN_SANDBOX_REAPED
job=164155a4-8739-43e3-87b3-6739ae1891c9 status=queued attempts=1 ... epoch=2 blocked=false
P0_RETRY_REAL_PDFLATEX_FINISHED
P0_REAL_GVISOR_WORKER_KILLED_RECOVERED_AND_LATEX_SUCCEEDED
P0_DURABLE_QUEUE_REAL_SIGKILL_RECOVERY_PASS
P0 disposable scopes, containers and workspace verified cleaned.
```

**其中：** 首次 SIGKILL 目标为真实 Go Worker 进程；原 gVisor Sentry 正在运行。新 Worker 使用数据库给出的旧 SandboxID 执行 `runsc list/kill/delete`、检查 `null-netns` 挂载及两个 systemd Scope；只有确认清理完成才在 PostgreSQL 提交 `FinishRecovery` 并释放全局槽位。第三个 Worker 领取同一源码快照，用不同的 Sandbox ID 真实运行 PDFLaTeX，输出有效 PDF。最后数据库确认任务 `succeeded`、`attempts=2`、`epoch=3`，槽位空闲且未被 blocked。

**单独的正常路径实机测试也已通过：** `pdflatex` 编译得到 1 页、31,860 字节 PDF；任务 `succeeded`、`attempts=1`、`epoch=1`，scope 清理成功。

## 四、本轮新增/修改的代码

- `internal/platform/papersassets/store.go`：P0 本地内容地址登记；源码快照绑定项目 UUID/revision；拒绝路径穿越、非法 UTF-8、不受信环境或篡改；环境清单必须匹配固定 gVisor 版本和 TeX Live 镜像 Digest，且离线归档每次使用前重新计算 SHA-256。不可变小文件使用临时文件、fsync、hardlink 原子发布，避免并发观察到部分内容。
- `internal/platform/papersworker/executor.go`：执行适配器。任何 OCI Bundle 生成和真实沙箱启动之前先验证源码与环境；随后按任务的随机 SandboxID 建立新目录、展开可信离线 TeX rootfs，写入固定策略的文件权限，使用原有 `paperssandbox.Supervisor` 运行并核查清理。恢复时按旧 SandboxID 创建 ScopeBackend，**不依赖丢失的输入文件仍然可尝试回收**。
- `internal/platform/papersqueue/claim.go`：固定 Sandbox ID 编码。原有 token/epoch fencing 与 DB 全局互斥策略保持。
- `cmd/papers_p0_durable_worker/main.go`：仅本机 P0 使用的独立操作系统进程入口，严格约束 root、显式确认标志和单独随机端口的测试数据库。
- `scripts/papers/p0-durable-worker-kill-e2e.sh`：实机测试自动化、真正的二进制 SIGKILL、数据库租约过期检查、恢复、再次编译、测试资源回收；专门拒绝虚假成功。
- `internal/platform/papersassets/*_test.go`、`internal/platform/papersworker/*_test.go`：并发、校验失败、权限、篡改和模拟的真实执行适配器测试。

## 五、未通过的生产上线路径

本轮**没有实现生产级环境仓库、授权 Worker 服务或正式用户编译入口**。仍需：

1. 改造测试用 root 身份为独立的**最小权限 Launcher** 和受限 Worker 服务身份，限制宿主命令调用、scope 操作和文件系统权限；禁用对业务 API 的 root/Docker Socket 访问。
2. 将 P0 本地快照资产目录替换为可认证、私有、带版本和访问控制的对象存储；将环境构建与签名或不可变注册表接入，不能让源文件或环境路径来自用户任意参数。
3. 从受控、已发布 TeX 环境提供引擎矩阵：TeX Live 2025、2026；pdfLaTeX、XeLaTeX、LuaLaTeX；Biber/BibTeX；NeurIPS/ACL/ICLR 模板；字体与依赖扩展。
4. 持续防御性测试：数据库短暂失联、Launcher crash、父进程/Scope 僵死、磁盘写满、软/硬资源上限、输出尺寸与归档遍历、环境包中恶意符号链接等。
5. 长期 Worker 进程、健康监控、重启恢复调度循环、最小配额、可审计资产发布和产物分发；现只支持 P0 单次 `Runner.Step`。
6. MinIO/相关服务网络暴露及生产权限、隔离与总资源容量的 P0 额外阻断事项。

**最终判断：** 「一次性 PostgreSQL 与真实 gVisor 环境中的 SIGKILL → 持久租约失效 → 新 Worker 确切清理旧沙箱 → 恢复队列 → 实际重新编译 PDF」已通过实机验证；**Papers 生产开放仍为 No-Go**。
