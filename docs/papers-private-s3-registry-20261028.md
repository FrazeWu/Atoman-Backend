# Atoman Papers — P0 私有 S3 不可变清单注册表与并发写入验收

日期：2026-10-28。主机：`vmi3410530`。独立分支：`feat/papers-s3-conditional-registry-20261028`。
**发布状态：Papers P0 总体 No-Go。** 本阶段仅实现可信源码快照、固定编译环境的私有 S3 *清单* 注册表和对现有受控 Worker 的读取接口适配。没有修改正式 MinIO 桶、业务存储账户或服务器服务，不提供公开 API。

## 1. 安全技术决策

仓库现有 AWS SDK for Go v1.45.11 的 `s3.PutObjectInput` 没有独立的 `IfNoneMatch` 字段。升级整套 SDK 可能扩大变更面。已实测可利用 v1 的 `PutObjectRequest` 及请求 `Handlers.Build.PushBack`，**在 SigV4 签名之前**注入 `If-None-Match: *`。存储服务必须在对象写入时原子判定同名键是否存在，不能使用 `HEAD + 普通 PUT` 的竞争窗口替代。

官方参考：
- [AWS S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html)
- [AWS S3 enforce conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes-enforce.html)
- [MinIO AIStor S3 compatibility](https://docs.min.io/aistor/developers/s3-api-compatibility/)

### 分层接口

- `internal/platform/papersassets/s3_conditional.go`：AWS SDK v1 底层条件 PUT / GET、412 特例、受限读取大小、桶版本化与匿名读取安全探测；不设置 public-read ACL，不提供删除和公开链接生成接口。
- `internal/platform/papersassets/s3_registry.go`：固定 `papers/immutable/v1/{snapshots,environments}/<sha256>.json`、固定清单类型、专用私有桶与公共共享桶名称必须不同，禁止任意路径/键；验证内容 SHA-256 和清单合法性。
- `internal/platform/papersassets/compile_registry.go`：`CompileAssetReader` 只允许加载可信项目快照与核验固定离线归档；同时由原本地存储和新 S3 私有注册表实现。
- `internal/platform/papersworker/executor.go`：仅将 `Config.Store` 从具体本地类型替换为只读 `CompileAssetReader`，保留数据库项目/版本校验、运行前校验、固定 gVisor Supervisor 及恢复逻辑；本次**没有启用生产 S3 配置或修改现有进程入口**。
- 源码快照依旧限制 4 MiB、64 文件、UTF-8、受控扩展名、不可重复路径和必需的 `main.tex`；环境清单仍强制固定 TeX Live 2026 镜像 Digest 和 gVisor release。

### 发布前能力探测，任何失败均 fail-closed

调用 `Probe` 会执行：
1. 已配置专用非公共桶，与现有 `S3_BUCKET` 不相同，且 `GetBucketVersioning` 返回 `Enabled`；
2. 随机创建 `papers/immutable/v1/_probes/<nonce>`，第一次条件写入成功；
3. 对**相同键写入不同内容**，必须实际返回 HTTP 412；
4. 重新读取原键，必须仍是第一次写入的精确字节；
5. 使用**匿名、无任何 AWS 凭证**的 GET，必须得到 HTTP 403；200、404、连接失败均不得作为验证通过。
6. 所有检查完成后才将写入能力标记为已验证，任何未知/不支持的 S3 行为一律拒绝发布。

实际的 S3 角色必须允许 Publisher 的 `GetBucketVersioning`、`GetObject` 和 `PutObject`，且在独立桶和固定前缀内限制。Root Launcher 的读取角色应只有 `GetObject`，不需要、也不能获得发布能力；只读 `LoadSnapshot/LoadEnvironment` 不需要 `Probe`。

### 并发与故障处理

- 新发布、重复发布均采用 S3 端原子条件写。重复键在服务端返回 412 后只能通过**带凭证的 GET + SHA-256 + 精确字节一致性**确认同一不可变对象；遇到相同键但内容不符即 `ErrIntegrity`。
- HTTP 200 也必须经过读取验证，不能只凭 200、ETag 或密钥路径返回成功。
- 网络响应丢失、S3 409/429/5xx 等可安全重试的错误采用**最长 4 次条件 PUT**、小幅有界退避，并始终验证回读；403/未授权类错误立即返回，不能被现有同内容对象掩盖。
- 如果 S3 忽略 `If-None-Match`、匿名 GET 被允许、桶不支持版本化、读回错误、对象内容被篡改，立即拒绝，不作非原子降级。
- 本地清单发布也修正为排序前复制文件切片，避免修改调用者共享的文件数组。

## 2. 真实一次性 MinIO 验收

测试脚本：`scripts/papers/p0-private-s3-registry-e2e.sh`。

- 临时 `minio/minio:latest` 容器，`--tmpfs /data`，只绑定 `127.0.0.1` 随机端口；独立随机测试凭证；禁止特权容器、capabilities；不读取生产凭证；退出自动删容器与测试桶。
- 测试在容器内新建名字为 `papers-private-p0-...` 的随机桶，显式启用版本化；无公开读取权限。
- **24 个并发 goroutine** 对同一快照条件发布均成功，所得 Digest 相同；第二次写入不同字节得到 412。
- **6 个独立 Go 进程**，各自重新创建 AWS SDK 客户端/探针，并发提交相同项目/Revision/源码快照，均成功发布并返回相同有效对象。
- 桶默认匿名读取返回 HTTP 403；故意给另一临时桶配置公共读取 policy 后，能力探测拒绝；未启用版本化的临时桶同样拒绝。
- 以一次性管理员账户故意普通 PUT 篡改当前对象版本，后续 `LoadSnapshot` 和重新发布均得到 `ErrIntegrity`，不会把篡改结果当作成功。
- 单元测试额外覆盖：忽略条件头的 S3 兼容实现、错误路径/不合法桶/非法清单、未授权 PUT、响应丢失后安全回读、内容排序稳定性、本地归档 Digest 与符号链接检测。
- MinIO 首轮 24 并发时曾出现 `server closed idle connection`，初版注册表正确报错；加入有界、条件写入的安全重试及内容核验后已复验通过。

最终真实验收标记：

```text
P0_PRIVATE_MINIO_LOOPBACK_READY
P0_S3_CONDITIONAL_24_WRITERS_PRIVATE_READ_DIGEST_TAMPER_DETECTED
P0_S3_CROSS_PROCESS_6_CONDITIONAL_PUBLISHERS_PASSED
P0_S3_PUBLIC_BUCKET_PROBE_REJECTED
P0_S3_UNVERSIONED_BUCKET_REJECTED
P0_PRIVATE_S3_REAL_CONDITIONAL_WRITE_E2E_PASS
P0_ISOLATED_MINIO_CONTAINER_CLEANED
```

## 3. 上线仍未通过的硬门槛

1. **对象存储管理员仍有权普通 PUT 篡改现行版本**（本次故意验证）。版本化只提供恢复能力，并非严格 WORM。生产必须先启用 Object Lock/合规性保留、仅对受控对象生命周期开放写入，并通过 IAM/Bucket policy 强制 `s3:if-none-match`、拒绝 DeleteObject/DeleteObjectVersion；在目标 MinIO 部署上实测策略兼容性，不直接套用 AWS 专有 Condition Key。
2. 私有桶生产环境需要 TLS 证书、传输加密和服务端加密、备份与对象保留策略、稳定的访问控制与专用短期密钥（Publisher 与 Reader 严格分开）、密钥轮换/审计，以及持续探测公共读取和配置漂移。此次 `http://127.0.0.1` 只用于本机隔离 MinIO。
3. 本阶段仅把**小型可信清单**迁入 S3。离线大型 TeX Live rootfs 归档和实际 PDF/图片资产仍未实现经过批准的 S3 发布/验证/下载/限额链路。不可把用户上传的任意 TAR/ZIP 当作受信环境镜像解包。
4. 固定 TeX Live 2026 与 gVisor 的 P0 环境清单仍是测试用钉住版本；生产需要可信环境构建、源包签名、镜像来源证明、XeLaTeX/Biber/ACL/NeurIPS 等版本兼容矩阵。
5. 数据库 `paper_compile_jobs` 已存快照/环境 Digest，但没有完成**真实生产 Publisher → 数据库事务 → 限权 Launcher → 私有 S3** 的整合验收与系统服务部署。
6. MinIO 容器使用测试镜像标签 `latest`；正式部署必须固定镜像 Digest、完成兼容测试和升级/回滚演练。一次性测试容器被完整删除；主机现有 MinIO 服务未改动。

**最终结论：本轮私有清单注册表的条件写入、并发、一致性、匿名拒绝、版本化及异常完整性子项通过。Papers 整体 P0 仍为 No-Go。**
