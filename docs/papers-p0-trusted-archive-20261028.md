# Atoman Papers — P0 可信 TeX 环境归档发布与私有存储策略验收

- 日期：2026-10-28
- 服务器：`vmi3410530`
- 工作分支：`feat/papers-trusted-archive-20261028`
- 祖先提交：`d82d009`（私有 S3 条件写入清单注册表）
- **总体结论：P0 子项实测通过，Atoman Papers 生产依然 No-Go。**

## 1. 本次实现边界

原先已有 `PrivateS3Registry` 可在独立私有桶中发布至多 4 MiB 的 JSON 清单，具有 S3 `If-None-Match: *` 条件 PUT、读回 SHA-256、匿名 403、版本控制验证。但离线 TeX Live rootfs 尚仅保留在本机固定 TAR 文件，未有可验证的大型二进制发布、签名和私有下载缓存。

本轮新增：

1. `trusted_archive_transport.go`：复用项目已有 AWS SDK Go v1 的 `s3.PutObjectRequest`，签名前插入 `If-None-Match: *`，通过 `*os.File` 的 `io.ReadSeeker` 进行单次条件 PUT，不将整个归档加载到内存；私有 GET 返回流和长度。**最大 4 GiB**，低于 S3 单次 PutObject 5 GiB 上限；更大的环境必须另行审计 Multipart Complete 的条件写入。
2. `trusted_archive.go`：`ArchiveClaim` 绑定 Environment Digest、完整的固定 `Environment`、TAR 字节数、Ed25519 发布者密钥 ID。签名前使用稳定字段顺序的 JSON + domain separation `atoman-papers/trusted-tex-archive/v1\0`。`SignedArchiveRelease` 存入 `papers/immutable/v1/trusted-releases/<environment SHA-256>.json`，归档存 `papers/immutable/v1/trusted-archives/<archive SHA-256>.tar`；全部条件写入、读取核验。
3. `trusted_archive_cache.go`：可信读端加载签名清单，验证已配置的 Publisher Ed25519 **公钥**及固定 gVisor/TeX 环境，再以受限流式 GET 下载到真实、私有（0700）、由运行身份拥有的缓存目录。下载中的临时文件无暴露；SHA-256、确切大小、fsync、hardlink 原子发布、只读 0400、目录 fsync；已存在的目标永不覆盖，需再次校验。
4. `TrustedCompileAssetReader`：适配现有 `CompileAssetReader` 接口。根 Launcher 若显式配置为它，将从私有 S3 读取源码快照，并且**必须验证发布者签名、离线 TAR 字节数和 SHA-256**，才能允许现有 gVisor Worker 解包。以前的本地 P0 `Store` 保持不变；没有自动切换生产 Launcher。
5. `trusted_bucket_policy.go`：**仅供 AWS 审查的 Deny-only JSON 参考**，拒绝未携带 If-None-Match 的目标前缀 PUT、拒绝普通 DeleteObject/版本删除、拒绝非 TLS 请求。该策略不含 Allow，不会自动安装到任何桶，**不承诺直接兼容 MinIO**。MinIO 真实策略匹配必须单独做负向测试。
6. 归档预扫描 `ValidateTrustedTar`：要求安全路径、无绝对路径或父目录逃逸、无逃逸链接、拒绝设备节点/Socket/FIFO、每个 TAR 项与总量均有界；只针对经过独立批准的 TeX 构建产物，**不得作为用户 TAR/ZIP 的通用解包器**。

## 2. 发布前必要条件

- 专用私有桶且不同于公共 `S3_BUCKET`；必须已启用版本控制，匿名读的探针对象须得到 403，条件重复写入不同字节必须得到 412，原字节不变；
- **Object Lock 已开启，默认保留模式必须为 COMPLIANCE，最小 Days 由调用方显式指定**；GOVERNANCE 不会通过。
- 发布者持有 Ed25519 私钥，并且该私钥的公钥必须是读端预先固定的可信根之一。私钥绝不可存于 root Launcher 的进程环境变量或项目资料。
- 归档源文件必须为本地普通文件、不可为符号链接、不可被组/其他用户写入；验证 TAR 项路径以及流式 SHA-256 与固定环境清单匹配。归档在首次 PUT 和读取回验后才发布带签名的 release。
- 网络不确定的重试仅允许再次使用条件 PUT；遇到 412 的已存在键，必须读取原对象并验证内容。若被管理员篡改，则报告 `ErrIntegrity`。

## 3. 实际 MinIO 2026-10-28 隔离测试

所有实验使用 `scripts/papers/p0-private-s3-registry-e2e.sh`：一次性 MinIO 容器、`--tmpfs /data`、本机 `127.0.0.1` 随机端口、随机凭证、独立测试桶。退出自动删除测试容器及其存储，不读取生产 MinIO 的密钥。

**实际通过的子项：**

- 原 S3 注册表的 **24 goroutine** + **6 个独立操作系统进程**同一 Digest 并发条件发布；
- 生成测试用 Ed25519 密钥对，发布签名清单；从独立私有 S3 桶流式上传 **5 MiB 以上** 的模拟 TeX TAR（大于 4 MiB 小型 JSON 清单限制），再次下载、内容地址与签名检查通过；
- 创建测试时显式设置 `ObjectLockEnabledForBucket=true`、`DefaultRetention=COMPLIANCE,Days=1`，获取 Object Retention 证明为 COMPLIANCE 且到期日在未来；
- 针对目标版本执行 `DeleteObject(...,VersionId=...)` 得到 MinIO `400 InvalidRequest`；**再次根据原 VersionId GET 成功**，确认受保护版本确实没有被删除；
- 如果桶配置的是 `GOVERNANCE` 而非 `COMPLIANCE`，`ProbePublisher` 正确拒绝；
- 原注册表针对公开匿名 GET、未启用版本控制，以及忽略条件写入的服务端均拒绝；
- 单元测试模拟恶意 TAR 路径穿越、设备节点、符号链接逃逸、发布者伪造签名、私钥/公钥不匹配、篡改二进制、缓存已存在符号链接、读取中断/长度不符/被修改对象、世界可写源文件/缓存目录，均拒绝或返回完整性错误。

实机最终标记：

```text
P0_S3_CONDITIONAL_24_WRITERS_PRIVATE_READ_DIGEST_TAMPER_DETECTED
P0_S3_CROSS_PROCESS_6_CONDITIONAL_PUBLISHERS_PASSED
P0_S3_PUBLIC_BUCKET_PROBE_REJECTED
P0_S3_UNVERSIONED_BUCKET_REJECTED
P0_TRUSTED_ARCHIVE_SIGNED_S3_COMPLIANCE_RETENTION_CACHE_VERIFIED
P0_TRUSTED_ARCHIVE_GOVERNANCE_NOT_COMPLIANCE_REJECTED
P0_PRIVATE_S3_REAL_CONDITIONAL_WRITE_E2E_PASS
P0_ISOLATED_MINIO_CONTAINER_CLEANED
```

### 关键区别：Object Lock 不等于当前键不可覆盖

AWS 和 MinIO 的 Object Lock 保护的是**对象特定版本**。COMPLIANCE 阻止在保留期内永久删除受保护版本，**但不能凭它单独阻止写入同名键的新版本或创建 Delete Marker**。要保护当前键必须在存储服务端额外实施符合目标实现的条件写入和删除禁止策略；代码仍需在每次读档案时校验签名与 SHA-256。

官方依据：
- https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html
- https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes-enforce.html
- https://github.com/minio/minio/blob/master/docs/bucket/retention/README.md

## 4. 生产级 IAM / 服务安全设计（只读草案）

- **环境构建/发布者**：独立可信 CI/Release Runner，密钥在受控 HSM/KMS/机密管理器内；仅指定 private bucket 和前缀的受控 `PutObject`、`GetObject`、`GetBucketVersioning`、`GetObjectLockConfiguration`；不允许删除、解除保留、公开 ACL。对条件写入必须有**服务端 IAM/bucket policy 强制执行**，而非仅靠应用头。
- **Root Launcher**：专用 S3 *只读*身份，仅访问自身需读的内容地址，禁止 `PutObject` / `DeleteObject` / `DeleteObjectVersion` / `PutBucketPolicy` / `PutObjectRetention`，不应持有发布者私钥。
- **数据库 Worker**：不持有 rootfs 归档凭证；只持有已授予的队列 DB 身份与特定 Unix Socket 权限。
- **S3 桶**：和公开媒体桶完全隔离，开启版本控制、COMPLIANCE 保留与 TLS、服务端加密、审计；明确拒绝匿名/公共读取、无条件覆盖与删除标记；对后台独立备份与对象生命周期单独设计。
- **管理端 / 服务端**：AWS 参考 Policy 并非 MinIO 的兼容证明。目标 MinIO 必须使用**非管理员的专用 Writer/Reader 账户**进行真实无条件 PUT、DeleteObject/DeleteObjectVersion、ACL/匿名访问等负向测试，只有在被禁止的动作均返回拒绝且正向读写成功时才可以上线。

## 5. 尚未通过的上线门槛

1. **完整的 TeX Live rootfs 归档**尚未通过新版 TAR 预扫描、签名发布、对象缓存、解包与真实 gVisor PDFLaTeX 的端到端验证；实机用的是 5 MiB 模拟 TAR。实际环境中可能有绝对 symlink 或设备节点，需要由专用可信打包流程处理，不允许放宽安全过滤来强行通过。
2. 尚未在真实目标 MinIO 上安装、模拟和验收严格的生产 IAM/Bucket Policy；没有验证 delete marker 和无条件写入在生产专用账户下都被拒绝。当前使用的是一次性 MinIO **管理员级测试凭证**，生产不得复用。
3. AWS `PutObject` 单次写入 5 GiB 限制下，本阶段只支持至多 4 GiB 的归档；更大的 TeX Live 快照需实现和审计 Multipart 上传的 `If-None-Match` 条件完成，或按可信环境计划拆分。
4. 签名密钥生命周期、密钥轮换/撤销、签名有效期、重新签发、分离的生产 CI、制品发布审批、完整事件审计尚未实现。
5. 真正的 `Papers Queue → TrustedCompileAssetReader → SignedRelease → Downloaded rootfs → gVisor` 尚未作为持久服务上线或全链路验收。已有根 Launcher 的 P0 CLI 不会自动启用新对象存储。
6. 按当前任务 worktree 实施，不修改正在运行的服务，不合并/推送不完整的原型分支，也不更改现有 `main`。

**最终判断：可信环境的大型归档条件写入、签名清单、完整性保护、缓存原子发布与 COMPLIANCE 版本保护已经获得一次性实机验证；整体 Papers P0 保持 No-Go。**

## 6. 复核后的附加安全修复与验收

在原有 Ed25519 / S3 / COMPLIANCE 测试之外，新增下列有证据支持的加固：

- `ValidateTrustedTar` 不仅拒绝 `../` 和绝对路径，还拒绝**任意顺序**的“归档符号链接作为其他文件路径祖先”情况，防止 root 执行 `tar -xf` 时由于链接路径重定向写出根目录；拒绝重复路径、指向未创建文件或符号链接的硬链接、SUID/SGID 文件，以及不受支持的特殊节点。预扫描最多 50 万条目，逐路径检查按条目与目录深度增长，不使用二次方全量遍历。
- `internal/platform/papersworker/executor.go` 增加可选 `ArchiveFetcher`：只用 PostgreSQL 已授权任务绑定的 Environment Digest 请求缓存。签名/下载失败或缓存路径校验不通过时，**不回退到原先本地 TAR**，不创建新沙箱。已有本地 P0 路径不变；真正的生产 Launcher 尚未自动启用该缓存接口。
- `trusted_tar_edges_test.go`、`trusted_archive_fetcher_test.go` 增加链式路径和 Worker 防降级测试，均通过 Race Detector。
- 实时 MinIO 增加**未受保护对象的版本删除对照试验**：同一 SDK 对普通版本执行版本 ID 删除可成功，而测试 COMPLIANCE 版本删除请求返回 MinIO HTTP 400 `InvalidRequest`，该版本随后仍可通过 VersionId 读取；已观察到拒绝，但不能把状态码 400 跨提供商等价解释为通用的 403。
- 测试项目具有回环端口、一次性随机凭证与 `tmpfs`，不依赖或更改生产 MinIO 存储桶。整轮实机测试重复成功，且没有遗留本轮容器。

**重要保留：** 这是小型、受信模拟 rootfs 的实机发布与校验结果。尚未对实际 TeX Live 镜像导出的完整 rootfs 使用最终安全预扫描工具进行发布、缓存和 gVisor 编译的单一端到端验收。签名只能证明经批准发布者的身份与归档字节完整性，**不能单独证明镜像构建来源、包依赖安全性或供应链可信度**。
