# Atoman Papers — P0 真实 Launcher 故障恢复验收记录

**日期：** 2026-10-28

**机器：** `vmi3410530`

**分支：** `test/papers-launcher-live-20261028`
**状态：** 特定场景实机验收通过；整体 P0 / 生产上线 **No-Go**

## 1. 范围与依赖

本轮目标：使用新开发的 Go `paperssandbox.Supervisor` 和 `ScopeBackend`，而非仅调用过去的 shell 冒烟测试，验证**真实 gVisor PID 超限 → 正确事件归因 → 彻底清理 → 下一次真实 LaTeX 编译成功**。

| 组件 | 实际固定版本/设置 |
|---|---|
| gVisor | `runsc release-20261019.0`，systrap，`--network=none --overlay2=none --systemd-cgroup` |
| 下载校验 | 官方发布包 `gvisor.tar.zstd`，SHA-512 本地及官方校验文件两次通过 |
| gVisor SHA-512 | `bcbf431b2f9a039f6ab23bfe23986031130c0f149294978cedfd46910901deba0737d8642c848f597ed4b5c72324885db4d75ee86a1f6a90915fbe1f2f2eb915` |
| TeX Live | `texlive/texlive@sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4`（TeX Live 2026） |
| 沙箱身份 | UID/GID 65534，`noNewPrivileges`，零 Linux capabilities |
| 沙箱文件系统 | 镜像目录为只读 rootfs；仅明确的 `/output` 和限额 tmpfs 可写 |
| 任务资源 | PID 压力任务 `pids.max=128`；LaTeX 任务 `pids.max=256`，内存 768 MiB，禁用 Swap，1 CPU 配额 |
| 正式编译参数 | `pdflatex -no-shell-escape -interaction=nonstopmode -halt-on-error -output-directory=/output main.tex` |

独立任务 workroot 使用 `/var/tmp/atoman-papers-p0-live.XXXXXXXX`，将镜像导出一次并通过只在同一文件系统使用的 hardlink 拷贝构造两个独立只读 rootfs；两个任务的 bundle/state/output 目录和 systemd scopes 均独立。文件内容、诊断程序与 OCI spec 均由受控测试程序生成，**不来自用户输入**。无 Docker Socket 或主机密钥挂载到沙箱。

## 2. 真实结果

2026-10-28 的一次完整实机运行：

| 顺序 | 任务 ID | 观察信号 | 最终结果 |
|---|---|---|---|
| 1 | `p0-pids-8c97d241` | `runsc exit=2`，Sentry `pids.events.max=1`；资源限制验证成功，cleanup verified | `resource_exhausted_pids` |
| 2 | `p0-tex-8c97d241` | `runsc exit=0`；Sentry cgroup 限制验证成功；生成有效 PDF 头；cleanup verified | `completed` |

关键终端标识：

```
P0_LIVE_PID_FAILURE_THEN_REAL_LATEX_SUCCESS
P0_LIVE_FULL_SEQUENCE_PASS
P0 cleanup verified; no test-owned processes, mounts or files.
```

结果含义：在真实 PID 压力测试中，gVisor 非零退出不阻断下一任务，**前提是** Supervisor 将可信 `pids.events` 归因为资源耗尽，并显式验证沙箱、namespace 挂载和 systemd Scope 已清理。下一任务是真实 PDFLaTeX，而非模拟成功返回。

这里的 PDF `ArtifactVerified=true` 表示检测到本次新生成的、大小受限且以 `%PDF-` 开头的常规文件；**尚未做完整 PDF 语法/语义验证**。

## 3. 新增代码

- `internal/platform/paperssandbox/p0_bundle.go`：平台可信输入生成窄化 OCI config；固定 TeX 编译参数，不执行 Shell；在**明确启用测试模式时**仅允许 `/inputs/p0-sandbox-probe pids`，普通编译政策不放行该测试程序。
- `cmd/papers_p0_live_runner/main.go`：仅供 root 且设置显式 P0 确认标志的本机 CLI；用同一个 Go Supervisor 顺序处理两个独立 ScopeBackend；只有 PID 任务被正确归因并清理后才运行真实 TeX 编译。
- `scripts/papers/p0-launcher-live-e2e.sh`：固定版本下载/校验、受控镜像导出、OCI 工作目录创建、执行和清理。脚本不连接生产 PostgreSQL，不安装全局 `runsc`，也不修改 Docker 默认运行时。
- `internal/platform/paperssandbox/oci_policy.go`：增加测试专用 PID probe 的严格白名单，默认规则仍只允许固定 `pdflatex` 编译参数。
- `internal/platform/paperssandbox/p0_bundle_test.go`：验证 OCI 自检、默认拒绝诊断程序、不允许覆盖现有配置、拒绝不安全路径与模式。

## 4. 安全边界与仍未完成项

**不能因为此次 P0 测试通过就直接开放编译 API。** 尚需完成：

1. 生产级权限受限的 Launcher 服务、可审计独立身份；目前 P0 测试准备阶段使用本机 root，绝不能将其暴露给网络请求。
2. PostgreSQL 持久任务队列、跨进程租约/fencing token、孤儿任务重启恢复；当前 Supervisor 的排他锁仅在单个 Go 进程内有效。
3. 系统安全基线和编译引擎矩阵：TeX Live 2025/2026、XeLaTeX、Biber、字体和真实 NeurIPS/ACL/ICLR 模板。
4. 强对抗性 LaTeX 输入、路径逃逸、磁盘/输出限额、压测、公平调度、日志截断与中途故障恢复验证。
5. MinIO/端口暴露、现有服务的最小权限及故障隔离。
6. 安全的生产 OCI 环境构建/发布、可信镜像和依赖 Digest 管理。

**结论：** 本次可把「PID 压力导致 runsc exit=2 后，Go Supervisor 清理并恢复下一次真实编译」记为 **已通过实机验收的子项**。生产 Papers 的总体 P0 仍保持 **No-Go**。

## 5. 复现方式（仅用于隔离测试机器）

在测试已缓存锁定 Digest 镜像的前提下：

```sh
cd /root/Atoman/.worktrees/papers-launcher-live-20261028-backend
bash -n scripts/papers/p0-launcher-live-e2e.sh
go test -race ./internal/platform/paperssandbox ./cmd/papers_p0_live_runner
PAPERS_P0_LIVE_CONFIRM=1 scripts/papers/p0-launcher-live-e2e.sh
```

请勿在生产请求路径触发此脚本；它会创建短生命周期的受限 systemd Scope，需要访问宿主机 Docker 和 gVisor 的 P0 维护权限。
