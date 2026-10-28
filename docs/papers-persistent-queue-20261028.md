# Atoman Papers — PostgreSQL 持久编译队列与租约恢复（P0）

日期：2026-10-28。工作区：`feat/papers-persistent-queue-20261028`（独立测试分支）。这是**仅内部可用**的队列与 Worker 调度基础设施，尚未注册 Papers HTTP API、未创建系统级 Launcher Service、未启动生产编译 Worker。

## 目标与保证

每项编译任务的项目/所有者、不可变源快照 Digest、LaTeX 环境 Digest、项目版本、主文件、任务优先级和幂等键在入队时固化；全局重量级任务槽位由 PostgreSQL 单行锁保护，避免多个进程同时进入编译阶段。

- 跨进程互斥：`paper_compile_slots(id=1)`，领取时 `FOR UPDATE SKIP LOCKED`；
- 先人工后自动：优先级 `10`（人工）高于 `0`（自动），同级按照创建时间与任务 ID；
- 任务持久化：`paper_compile_jobs`，幂等索引为 `(project_id,idempotency_key)`；重复提交得到同一任务；相同键但不同内容拒绝；
- 授权：内部入队要求项目归属于该 Owner、用户具有未过期邀请、入队的 `source_revision` 等于项目头版本；**领取任务时、租约心跳时、完成结果提交时以及过期恢复结束时再次核查**。撤销邀请/删除项目后，排队任务自动取消，运行任务不能保存成功结果；
- 真实的**源快照与环境**为独立的不可变对象，当前 P0 仅保存两者的 SHA-256 Digest，尚无已发布环境/快照注册表。因此不得接入用户请求或宣称生产编译可用；
- 同一时刻只能有一个全局重任务。旧租约过期后，下一次领取的不是新编译，而是旧任务的**受控清理任务**；
- 租约续期采用数据库时间 `CURRENT_TIMESTAMP`，避免多个 Worker 的本机时钟不同步；
- 使用双重 fencing：每次领取生成新的不可预测 `lease_token`，并把持久的单调 `epoch` 加一。任何旧持有者的心跳、结果提交和恢复确认都被拒绝；
- 收到取消请求时先持久化 `cancel_requested`，由 Worker 在执行时轮询并取消；清理完成后才能把状态写成 `canceled`；
- 清理不能核实则把单例全局槽位持久化设为 `blocked=true`，任何进程重启均不能自动跳过隔离状态。

## 模型

### paper_compile_jobs

包含：
`id`, `project_id`, `owner_id`, `source_revision`, `snapshot_digest`, `environment_digest`, `main_file`, `idempotency_key`, `priority`, `status`, `attempts`, `max_attempts`, `sandbox_id`, `worker_id`, `lease_token`, `lease_epoch`, `cancel_requested`, `failure_code`, `started_at`, `finished_at`, `created_at`, `updated_at`。

任务状态：

| 状态 | 说明 |
|---|---|
| `queued` | 已持久入队，未运行 |
| `running` | 已领取当前有效租约，执行或清理中 |
| `recovering` | 前 Worker 租约过期，仅允许回收其原 `sandbox_id`，禁止启动新编译 |
| `succeeded` | 编译及产物校验成功，且清理获证实 |
| `failed` | 已清理但编译或资源运行失败，或达到重试上限 |
| `canceled` | 已取消且相关沙箱完成清理 |
| `blocked` | 资源清理不可信，停止全局调度并等待人工处置 |

### paper_compile_slots

**只允许单条 `id=1` 的全局槽位记录**。含 `job_id`, `lease_token`, `worker_id`, `epoch`, `lease_expires_at`, `blocked`, `block_reason`, `updated_at`。Schema Migration 自动创建表，并使用 `INSERT ... ON CONFLICT DO NOTHING` 初始化这条记录；不会在重启时重置 Epoch、覆盖正在执行的任务或解除隔离。

## 领取/超时恢复流程

```text
Submit(可信已发布快照) → queued
ClaimNext:
  锁定全局槽位
  ├── blocked                        → 拒绝，ErrBlocked
  ├── 另一 Worker 的租约尚未到期    → 不执行
  ├── 原槽位失效但存有 job_id
  │     └── 更新 token + epoch → recovering
  │            ├── Cleanup(original sandbox_id) 成功
  │            │      ├── 已取消                 → canceled
  │            │      ├── attempts >= max       → failed
  │            │      └── 还有次数               → queued
  │            └── Cleanup 不可信             → blocked (全局停止)
  └── 槽位空闲
        └── 选优先级最高的 queued → running (全新 sandbox_id)
              ├── Heartbeat 每 lease/3 续租
              ├── 轮询 cancel_requested
              └── Supervisor.Run → 实际清理 verified
                     ├── 成功 → succeeded
                     ├── 已清理的失败 → failed / canceled
                     └── 未能核实清理 → blocked (全局停止)
```

无论任务执行结果如何，都不能以 `runsc exit=0` 或单凭 PDF 文件存在判定成功。成功记录同时要求 Supervisor 证明 `ArtifactVerified`、`LimitsVerified`、`SentryObserved`、`ExitKnown && ExitCode=0`、`CleanupVerified`，以及任务的当前 fencing 权限。

旧 Worker 继续运行时，其旧 `token/epoch` 无法提交成功或覆盖新记录；恢复 Worker 先通过可信 OS Launcher 清理**同一个旧 SandboxID**。清理前不得释放槽位。

## 本次代码位置

- `internal/model/paper_compile.go` — 两张数据表与索引；
- `internal/migrationrunner/runner.go` — schema、人工优先队列索引和全局单槽位初始化；
- `internal/platform/papersqueue/queue.go` — 参数校验、入队、幂等与邀请/所有权校验；
- `internal/platform/papersqueue/claim.go` — PostgreSQL 行锁、领取、租约心跳、恢复领取与恢复确认；
- `internal/platform/papersqueue/complete.go` — 成果归档、任务取消与 Owner 查询；
- `internal/platform/papersqueue/runner.go` — Worker 单次处理、周期心跳、Owner 取消通知与恢复后的确认；
- `internal/platform/papersqueue/*_test.go` — 真实 PostgreSQL 数据库的并发与故障测试。

## 验收与部署边界

在独立 Docker PostgreSQL 中执行：

```sh
cd /root/Atoman/.worktrees/papers-persistent-queue-20261028-backend
./scripts/test-isolated.sh ./internal/platform/papersqueue ./internal/migrationrunner
go test -race ./internal/platform/paperssandbox
go vet ./internal/platform/papersqueue
go build ./...
```

验收必须包括：

- 16 个并发 Worker 竞争单槽位，最多一个取得租约；
- 人工任务优先及重复幂等提交；
- 旧租约过期后进入 `recovering`，下一任务不能越过清理；
- 历史 token / epoch 的心跳与提交均返回 `ErrLeaseLost`；
- 正确清理恢复后新尝试分配**不同的 sandbox_id**；
- 超过尝试次数不能无限重试；
- 清理失败会持久化 `blocked`，新 Queue 实例仍禁止 Claim；
- Owner 只能操作自己的任务，运行中取消只设置请求位，不跳过清理；撤销邀请或删除项目后，未领取任务禁止启动，运行中的心跳拒绝续租，完成确认改为取消；
- Worker Runner 可在模拟的崩溃与恢复情景下正确调度、确认结果和拒绝虚假成功。

## 仍未实现的安全前置条件

1. 当前 Queue/Runner 是内部 Go 代码，**尚未接入真实 gVisor 工作区准备和 Worker 服务**。此前在 P0 实机验证中通过的是 Sandbox Supervisor 的独立恢复子项，不是此次持久队列的实际跨进程重启 E2E。
2. 不可变快照存储、TeX Live 环境 Registry、白名单依赖构建、产物分发与审计未接通，未经注册表校验的 Digest 不能被信任。
3. 需要生产级**最小权限** Sandbox Launcher 与稳定的任务/租约过期恢复轮询；当前只完成内部 Runner.Step。禁止把此 Runner 绑定 HTTP Handler、root API、Docker Socket。
4. 需要编译队列与全局环境构建任务采用同一个限额/串行准入，形成不可绕过的资源约束。
5. 需要 crash/restart 的真实进程故障注入、重复提交压力、锁等待测试及运行时报警；当前隔离数据库测试只证明事务与并发逻辑。
6. `blocked` 无自动恢复/解封 API，须待管理员确切核实残留资源与审计后设计专门流程。任何自动忽略 blocked 都是安全缺陷。
7. 仍需 TeX Live 2025/2026、中文字体、Biber、实际 NeurIPS/ACL/ICLR 模板与输入对抗测试。

**发布结论：No-Go。** 不合并至生产入口或启用用户编译，仅作为 P0 持久调度基础设施验证。

## 2026-10-28 跨 OS 进程真实 PostgreSQL 测试

新增只可在一次性数据库使用的 `cmd/papers_queue_p0/main.go` 和 `scripts/papers/p0-queue-process-recovery.sh`。

该脚本创建一个 **`127.0.0.1` 随机端口、`tmpfs` 数据目录、单独拥有并清理的 PostgreSQL 16 Docker 容器**，编译一个测试用的 Go CLI，然后让**多个独立操作系统进程**依次完成：

1. 进程 A：初始化两张 Compile Queue 表、唯一槽位、私有 Paper 模拟数据与任务；
2. 进程 B：领取第一次 `running` 租约后直接退出，不调用 Complete；
3. 通过该一次性容器内 `psql` 人为让测试租约过期；
4. 进程 C：领取 `recovering` 租约，**模拟**旧沙箱已清理后执行 FinishRecovery；
5. 进程 D：重新领取同一任务并使用**模拟成功结果**完成；确认新 SandboxID 与原先不同；
6. 进程 E：重新连接数据库，确认唯一任务 `attempts=2`、`status=succeeded`、槽位为空、`epoch=3`。

实测终端标识：

```text
P0_QUEUE_SEEDED
P0_OLD_LEASE_RECLAIMED_AFTER_MOCK_CLEANUP
P0_MULTI_PROCESS_POSTGRES_RECOVERY_PASSED
P0_DISTINCT_WORKER_PROCESSES_PASS
P0 disposable database and binary cleaned.
```

这是**跨操作系统进程共享 PostgreSQL 租约及持久化状态的验证**，而不是本脚本真的编译了 PDF：脚本没有启动 gVisor，旧沙箱清理和编译成功结果均为可信模拟。先前另一个 P0 子项使用真实 gVisor 完成 PID 超限及下一次 PDFLaTeX 编译的恢复测试，但两个子项仍需在最小权限的生产 Launcher Worker 中整合。

CLI 必须显式设置 `PAPERS_P0_QUEUE_CONFIRM=1`，且拒绝任何不是 `atoman_test@127.0.0.1:<随机非 5432 端口>/atoman_test?sslmode=disable` 的目标。无生产数据库写入，无 HTTP 入口；容器、测试二进制与临时文件在脚本退出时清理。

### 本次额外发现并修正

原版 Submit 先比对项目最新 Revision 再查询幂等键：文稿后续被编辑后，合法旧请求的重试会错误失败。现已改为**先核实所有权与邀请 → 查询完全匹配的既有幂等任务并原样返回 → 仅对全新任务校验最新项目头版本**。同时增加并发重复提交、重复恢复 fencing 与恢复期间取消的数据库回归用例。
