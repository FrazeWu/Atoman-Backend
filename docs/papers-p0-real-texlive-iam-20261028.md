# Atoman Papers — 真实 TeX Live、可信 S3、非管理员 IAM 和 gVisor 全链路验收（2026-10-28）

> 分支：`feat/papers-real-texlive-iam-20261028`；机器：`vmi3410530`；P0 disposable only。
> **最终判定：真实环境发布/下载/编译/故障恢复子项 GO；存储端服务强制不可覆盖策略 NO-GO；整个 Papers 生产 NO-GO。** 绝不将被拒绝的 MinIO IAM 条件键等价替换成未加防护的宽松权限并声称生产可用。

## 1. 固定环境与真实镜像预处理

- Docker 固定镜像：`texlive/texlive@sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4`；本机镜像 ID `sha256:7b4928ded471130598e1dcc5304930c51eb2c4a6da5bf932103c680e5da08696`。
- gVisor：`release-20261019.0`，外部发布的 zstd archive SHA-512 固定为 `bcbf431b2f9a039f6ab23bfe23986031130c0f149294978cedfd46910901deba0737d8642c848f597ed4b5c72324885db4d75ee86a1f6a90915fbe1f2f2eb915`；每次临时下载均检查预期值与官方独立校验文件。
- 原始 Docker export 实查 23,805 个归档项、569 个容器根目录意义下的绝对符号链接、12 处 SUID/SGID 权限位、1 个包含转义反斜杠且 P0 不需要的 systemd 单元文件。原始 TAR 不满足可信归档严格模式；未放宽通用扫描器。
- 新 `NormalizeTrustedDockerExport` **仅限已授权的 root P0 工作流**：将绝对链接转换为容器内等价的相对目标、移除 SUID/SGID 权限位、按完整固定文件名仅排除 1 个 systemd slice 单元，随后再次完整调用 `ValidateTrustedTar`。禁止目录穿越、设备节点、符号链接逃逸/作为其他文件父目录、硬链接越权和重复路径。中途错误会移除本次尚未公布的归档；成功只读 `0400`。
- 初次完整归档规范化结果 1,100,430,848 bytes，SHA-256 `660e24dd92f189168eb8ad189fad955c00f2cf907fbf440e479e8c8701da6669`。自包含的独立 Docker export 再次规范化仍得到 23,805 / 569 / 12 / 1，产物 Digest 不相同，证明**不能假设 Docker export 本身字节确定性**；最终签名绑定每一份规范化产物的**实际 SHA-256 Digest**，不假定两份导出 TAR 字节相同。未来生产 Environment Release 工具需要对构建及元数据作进一步可重现性审计。

## 2. 一次性私有 MinIO 与 IAM 实测

- 测试 S3 采用 `minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e`，`127.0.0.1` 随机端口，仅作用于本轮自动创建的 Docker 容器、临时数据目录、独立 bucket 和随机测试凭证；没有连接生产 MinIO 或加载现有服务 secrets。
- 创建仅本次可用的 **Publisher 和 Reader 两个不同的非管理员身份**；Reader 只允许固定 `papers/immutable/v1/*` 前缀的 GetObject，不得 PutObject / DeleteObject / 越权读取。Publisher 对此前缀允许 GetObject 与 PutObject、必要的版本化/Object Lock 配置信息读取，但没有删除权限。验证 Reader/Publisher 均无法 DeleteObject（包括 Delete Marker），Reader 无法写入，Reader 无法读取前缀之外的真实对象。
- 测试 COMPLIANCE Object Lock，避免将 GOVERNANCE 当作通过；对于受保护版本的删除被拒绝，而未加保留的控制版本可删除；Object Lock 是版本级保护，不能单独阻止新版本或 Delete Marker。
- 严格 AWS IAM 条件拒绝规则 `s3:if-none-match` 在固定 MinIO 版本安装时报 **`invalid condition key 's3:if-none-match'`**。所以该版本的身份策略**无法服务端强制所有 Publisher 只能条件 PUT**。
- 为了继续测独立身份和真实编译，实验环境中显式使用 `PAPERS_P0_IAM_UNSAFE_TEST_MODE=1`，仅在测试中删除不兼容的 Deny；独立负面实验证明 Publisher 可以无条件 `PutObject`。**它是有意识的“不达标再现”而不是安全替代策略，也绝不允许生产启用这个模式。** 真实客户端仍发送 `If-None-Match: *`，相同键条件重试返回 412，并读取验证 SHA-256，但无法防止获凭据者绕过客户端。
- P0 madmin 工具为 `scripts/papers/p0_minio_iam` 独立 Go 模块，只接受随机 loopback endpoint、`papers-private-p0-*` bucket 和测试用户名前缀，显式标志开启；不会持久化此账户或安装真实策略。
- 若目标仍为 MinIO，正式上线必须先提供**经实机证实**的服务端写入一次性强制措施（例如可审计的可信写入网关 + 后端网络隔离和后端凭证只在网关使用）；或者迁移至真正支持 `s3:if-none-match` Condition Key 的对象服务（AWS S3 官方支持）。任一种方案都必须针对非管理员身份验证普通 PUT、CopyObject、条件 PUT、删除标记与版本删除；本轮未对生产实施这类改造。
- 权限参考：<https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes-enforce.html>、<https://min.io/docs/minio/kubernetes/eks/administration/identity-access-management/policy-based-access-control.html>。

## 3. 真实 SignedRelease → S3 → root Launcher 只读缓存 → gVisor

- P0 offline Publisher 在内存生成 Ed25519 密钥对，以非管理员 Publisher IAM 身份条件发布私有源码快照、固定 Environment JSON 清单和 **完整 1.1 GB 根文件系统**；绑定 Environment Digest、原始归档 SHA-256、字节数、签发者 ID；签署后只留下受控的 **公钥** `trusted-publisher.pub 0400`，签名**私钥不传递到 root Launcher**。
- root Launcher 单独使用非管理员 Reader 凭证，通过 Unix Socket 获得当前有效 PostgreSQL lease；按数据库中的 snapshot/environment Digest 加载可信对象，先验 Ed25519、公钥身份、清单约束、SHA-256，再把完整归档以临时 0600 下载并验证后以原子硬链接发布到 root 私有缓存中的只读 0400 文件。Worker UID/GID 65534 不接触 S3 Reader 凭证、Publisher 凭证或本地归档。
- **为了证明没有旧文件降级路径，脚本在 Publisher 确认完成后删除了原始 `rootfs.tar`**；Launcher 必须从私有 S3 缓存完成 gVisor 解包。
- 实机使用完整 TeX Live 2026，实际 pdfTeX `3.141592653-2.6-1.40.29`，LaTeX2e `2026-06-01`，生成 1 页 `main.pdf` **31,860 bytes**；数据库 `succeeded,attempts=1,epoch=1,blocked=false`。
- P0 期间隔离的 `runsc --platform=systrap --network=none --overlay2=none`、只读 OCI rootfs、非 root 内部 UID、CPU/内存/PIDs 和 systemd scope 资源边界仍遵守原先 Supervisor 逻辑；此次不是重新审计所有内核逃逸风险。
- 非 root Worker **真实 SIGKILL** 时，旧租约仍 `running`、sandbox ID 留存；过期后独立 Worker 精确回收旧沙箱，epoch 递增至 2，再经新的签名 S3 环境重试真实 PDFLaTeX；最终后端验收标记 `P0_ROOT_LAUNCHER_UNPRIVILEGED_WORKER_REAL_RECOVERY_PASS`；测试退出 0，临时 Scope/containers/workspace 全部清理。

## 4. 自包含可复现实测脚本

`scripts/papers/p0-fullenv-s3-gvisor-e2e.sh` 用于**用户明确授权的本机隔离 P0**，并非生产服务：
- 需要 root、`PAPERS_P0_DURABLE_CONFIRM=1`、`PAPERS_P0_FULLENV_CONFIRM=1`、`PAPERS_P0_IAM_UNSAFE_TEST_MODE=1`（只表示使用此版本 MinIO 的 **未满足 IAM 条件** 的负面测试模式）。
- 默认 **不依赖外部手工 prepared tar**：从固定 Docker 镜像导出，在独立 workroot 经上述批准工具校验规范化，启动一次性 PostgreSQL + MinIO，发布并验证 S3、删除原始 TAR，启动 root Launcher 与非 root Worker，并自动回收每个标记匹配的测试资源。
- `PAPERS_P0_DURABLE_NO_KILL=1` 表示正常成功编译测试，省略该环境变量表示真实 Worker SIGKILL + 旧沙箱恢复测试；均已成功跑通。
- 可选 `PAPERS_P0_FULLENV_NORMALIZED_SOURCE=/var/tmp/atoman-papers-p0-fullenv.*/normalized-rootfs.tar` 仅是加快 P0 测试的受控输入，默认用自包含流程。
- 专项 MinIO IAM 测试 `scripts/papers/p0-iam-nonadmin-e2e.sh`；未设 `PAPERS_P0_IAM_UNSAFE_TEST_MODE` 则严格测试应失败，服务端拒绝 IAM condition；显式测试降级时只能判定“角色隔离通过但写入一次性策略未达标”。

## 5. QA / 仍须完成的正式上线内容

- **最终全部通过**：`go test -race` 覆盖 Papers assets、Worker、Launcher、Sandbox、常驻调度和三个 P0 CLI；`go vet ./...`、完整 `go build ./...`、Bash 语法检查通过；nested MinIO IAM Go 工具 `go test/go vet/go build` 通过；独立 PostgreSQL 回归 `GOFLAGS=-race ./scripts/test-isolated.sh ./internal/platform/papersqueue ./internal/modules/papers ./internal/migrationrunner` 退出 0，其中 migrationrunner 约 137.5 秒。严格 MinIO IAM condition 安装测试按预期失败，是独立于 Go 编译/数据库的上线安全阻断；不能将这项失败误记为通过。
- 未在任何生产 systemd 服务安装 Papers root Launcher 或 daemon Worker；原有 `main`、生产数据、当前 MinIO 桶策略和业务服务均未更改。
- 尚缺针对目标生产 MinIO 的不可变写入网关/条件策略真实验收；生产专用稳定系统账户、证书/TLS、凭证轮换、发布者密钥生命周期、对象存储监控与配额、审批和回滚流程，以及完整 TeX Live 版本升级矩阵。生产环境的正式发布程序还没有集成到主线。
- **结论：真实固定 TeX Live → 独立签名 Publisher → 私有 S3 → 非管理员 Reader → root Launcher → gVisor PDFLaTeX 的功能、基本完整性及故障恢复均实机通过；由于明确复现 MinIO 服务器无法强制无条件覆盖拒绝，整体 Papers 不能标记为生产 Go。**
