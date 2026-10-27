# Atoman Papers P0 — TeX Live/gVisor/cgroup 实测报告

- 日期：2026-10-27
- 机器：`vmi3410530`
- 任务分支：`test/papers-p0-texlive-gvisor-20261027`
- 阶段：P0 沙箱验证，**未通过完整上线门槛**
- 环境用途：独立离线测试；未修改生产 Docker 默认运行时，未接入 Papers API，未改动数据库、防火墙或现有业务服务。

## 1. 固定依赖与测试实现

| 依赖 | 固定版本 / 标识 |
|---|---|
| gVisor | `release-20261019.0`，`runsc` systrap |
| gVisor tar.zstd SHA-512 | `bcbf431b2f9a039f6ab23bfe23986031130c0f149294978cedfd46910901deba0737d8642c848f597ed4b5c72324885db4d75ee86a1f6a90915fbe1f2f2eb915` |
| TeX 镜像 | `texlive/texlive@sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4` |
| TeX 引擎 | pdfTeX `3.141592653-2.6-1.40.29 (TeX Live 2026)` |
| OCI 运行时 | `--platform=systrap --network=none --overlay2=none --systemd-cgroup` |
| 编译运行身份 | UID/GID 65534，`noNewPrivileges`，空 capabilities |
| 文件系统 | 只读根目录，仅显式输出挂载和限定大小的临时文件可写 |

测试代码：
- `scripts/papers/p0-gvisor-texlive-smoke.sh` — 构造一次性 OCI bundle、执行编译和资源测试、显式清理沙箱；
- `scripts/papers/p0-cgroup-scope-exec.sh` — 启动并观察独立 systemd 受限 Scope 与 gVisor Sentry Scope；
- `scripts/papers/p0-sandbox-probe.c` — 可信、有限规模的 CPU、内存、PID 和超时测试负载。

脚本要求显式设置 `PAPERS_P0_CONFIRM=1`，并在测试结束时清理测试专属容器、临时目录和 `null-netns` 挂载。测试脚本不得被 Papers Web API 暴露为通用编译服务。

## 2. 已验证结果

### A. 真实 LaTeX 编译 — 通过

- `pdflatex -no-shell-escape -interaction=nonstopmode -halt-on-error` 在离线 gVisor 中成功执行。
- 一页标准 `article` 文档生成 `main.pdf`，**41,292 bytes**，文件头 `%PDF-1.7`。
- 单次完成后沙箱清理成功，无残余测试专属容器。
- 不能据此推断 XeLaTeX、BibTeX/Biber、NeurIPS、ACL、ICLR 模板已通过。

### B. 内存与 Swap 限制 — 通过针对性测试

直接用普通 `runsc` 或仅限制外部 wrapper 时，约 192 MiB 的声明限制无法阻止沙箱写入 320 MiB。原因之一是当时实际执行 Sentry 的独立 cgroup 没有得到预期限制。

修正为 `runsc --systemd-cgroup`，并在 OCI 中同时设置：
- `linux.resources.memory.limit=201326592`（192 MiB）；
- `linux.resources.memory.swap=201326592`（对应 cgroup v2 `memory.swap.max=0`）。

实测 **Sentry** 的 `memory.max=201326592`、`memory.swap.max=0`，320 MiB 写入负载返回 **137**，不再成功完成。最终组合回归还观察到 `memory.events` 变化（例如 `max=28`、`oom=1`），能够与实际资源上限触发互相印证。测试证明硬限制可触发终止，但尚未对完整生产 Worker 做长期 OOM 恢复验证。

### C. CPU 限流 — 通过针对性测试

- Sentry `cpu.max=50000 100000`。
- 内核 `cpu.stat` 节流次数增长（示例 `nr_throttled=7`），说明真实执行组受到调度节流。
- 沙箱内进程 CPU/墙钟比例曾在约 0.65–0.79 区间，不能把它当成精确 50% 的主机 CPU 使用率。验收主要以实际 Sentry cgroup 限制和内核计数器为依据。

### D. 超时与明确清理 — 通过针对性测试

- 15 秒 sleep 负载被 2 秒 `timeout` 强制中止，`runsc` 包装层退出码 **124**。
- 超时后 `runsc list` 可能仍记录原沙箱；初始脚本曾因此报告清理失败。
- 新测试脚本在超时后针对**本次沙箱 ID**调用 `kill` 和 `delete --force`，并验证 `runsc list` 已无对应沙箱。
- 测试结束后卸载本次专属 `null-netns` 并清理临时文件。

该结果说明需要**显式生命周期回收**；不能假设 SIGTERM 或超时就自动销毁所有 gVisor 资源。

## 3. 未通过的 PID 压力验收

测试将 Sentry `pids.max` 设为 **128**，用可信负载尝试有限的子进程创建。

实测：
- Sentry 中读取到 `pids.max=128`；
- `pids.events` 的 `max` 计数从 **0 → 1**；
- 负载至少输出了创建 **16 / 32 / 48** 个子进程的进度；
- 随后 `runsc` 以 **2** 退出，测试负载未能给出完整的预期 `EAGAIN` 结果；
- 清理流程最终成功，没有残余测试 Scope 或临时目录。

结论：内核 PID 上限已真实触发，但 gVisor 在压力下终止，不满足本阶段对可控失败语义的预期。不得宣称 PID 压力测试完全通过。正式 Worker 必须能处理 Sentry 异常退出、将任务标记为明确的失败状态、清理环境，并验证下一项编译任务正常。

另外，`pids.max=16` 时沙箱连启动都失败，该值不可用作生产默认值。第一版可先考虑较高的启动容限，再通过受控负载与生产环境基线决定正式上限，不能仅凭本次测试拍板。

## 4. 其余尚未完成的 P0 门槛

- TeX Live 2025 和 XeLaTeX、Biber、中文字体、多文件及真实 NeurIPS/ACL/ICLR 模板；
- T1 字体支持：`latest-basic` 测试中使用 `fontenc[T1]` 触发 `mktexpk`，需要环境依赖策略与辅助工具白名单验证；
- 有限白名单 CTAN 依赖下载、签名校验、离线构建及环境 Digest 管理；
- 沙箱宿主挂载逃逸、输出大小、tmpfs/磁盘配额、并发公平性及长时间可靠性；
- 与生产 Atoman API、音乐/图书模块的性能回归与高压熔断；
- MinIO 9100/9101 对所有接口监听的访问面尚需确认与收敛；
- 现有业务 root 服务与凭据隔离、备份恢复；
- 正式 Sandbox Launcher 的最小权限身份、cgroup 监控、崩溃恢复和清理回归测试。

## 5. P0 发布判断

**No-Go。** 当前证明 gVisor/TeX Live 2026 可以完成最小离线编译，内存、Swap、CPU 与超时控制在针对性测试中生效，但 PID 压力下的稳定失败与恢复仍需开发；其他安全和功能验收尚未完成。

测试代码仅保存在隔离任务 worktree，未部署 Papers 编译 API，未安装系统级 `runsc`，Docker 默认运行时仍为 `runc`。用于本次测试的 TeX Live Docker 镜像已确认没有被任何容器引用，并已按确切标签删除；若需复现，先执行 `docker pull texlive/texlive@sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4`，再在独立环境中设置 `PAPERS_P0_CONFIRM=1` 执行脚本。测试程序的完整组合回归由于 PID 压力测试未通过而返回非零状态，这属于**预期的上线阻断**，不是测试脚本执行成功。

测试封装器进一步实施 fail-closed：必须观察到 Sentry systemd cgroup，并将 `memory.max`、`memory.swap.max`、`pids.max`、`cpu.max` 与 OCI 声明逐项比较；出现配置不符时返回专门失败码，不接受任何表面编译成功结果。

## 6. 后续优先级

1. 实现具有稳定任务 ID、故障状态、PID/Memory/cgroup 事件捕获和显式残留清理的 Sandbox Launcher 原型。
2. 将 PID 超限导致的 Sentry 异常退出转为可观测的受限任务失败，并复测下一任务能否正常启动。
3. 建立受控的 TeX Live 2025/2026 依赖锁定环境，验证真实学术模板与辅助程序白名单。
4. 完成 P0 网络/权限收敛和压测后才考虑生产集成、编译接口与 AI Agent。
