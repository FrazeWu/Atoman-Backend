# MusicBrainz 全量目录低空间重做计划

## 概述

在不恢复 MusicBrainz 原始数据库 schema 的前提下，将官方全量导出转换为 Atoman 的 `Artists`、`Albums`、`Songs`、`AlbumArtists` 和 `SongArtists` 数据。当前全量任务已经暂停并清理，下一轮必须先完成存储优化和样本容量验证，再启动生产导入。

完整的目录同步设计仍记录在 [`tasks/plan.md`](./plan.md)；本文专门记录低空间重做方案。

## 当前生产基线

- [x] 已终止暂停的旧同步进程，释放连接级 staging 临时表。
- [x] 仅删除本次 job `01a0468f-7ead-7158-b79e-1e4d930cf8a2` 产生的 2,968,143 位艺术家、2,600 张专辑、25,053 首歌曲和 27,653 条 revision。
- [x] 保留同步前的 15 位艺术家、51 张专辑、745 首歌曲和 937 条 revision。
- [x] 保留同步前已有的 31 张带 MusicBrainz ID 的专辑。
- [x] 对受影响表执行 `VACUUM (FULL, ANALYZE)`。
- [x] 已取消的 job 标记为 `failed / import`，不会继续复用其 staging 状态。
- [x] 清理后数据库约 1.65GB，主机可用空间约 108GB。

## 实际容量证据

旧方案的部分导入曾达到以下实际密度：

- staging 临时表约 21GB，其中 tracks 约 10GB、recordings 约 6.3GB。
- 艺术家表约 1.6GB，2,968,143 位艺术家已完成导入。
- 约 25,000 首歌曲已经产生约 28,000 条 revision；按当前行大小外推，歌曲 revision 可能额外占用约 50～60GB。
- 旧方案最终正式音乐数据预计约 135～145GB，包含 staging、WAL 和临时排序文件时峰值约 155～170GB，超过当前 148GB 文件系统的安全容量。

优化目标：

- 正式音乐数据控制在约 70～85GB 的量级。
- 导入峰值控制在约 80～100GB 的数据库占用量级，并保留至少 30GB 文件系统余量。
- 所有估算必须由样本实测校正，不能只依赖线性外推。

## 已确认的设计决策

- 目录导入不为每个歌曲和专辑创建完整的初始 revision 快照。
- 用户第一次编辑目录记录时，由 revision service 懒创建 baseline；后续编辑仍使用现有 revision 冲突和版本流程。
- staging 不在生产数据库中保留完整的 recording、track 和非代表 release 数据；只保留选择代表 release 和创建 Atoman 记录所需的字段。
- MusicBrainz ID 仍是规范来源标识；不得用名称覆盖或删除已有 Atoman 记录。
- 重复的 MusicBrainz 来源 URL 不逐行复制到数千万歌曲记录中；优先由已保存的 MusicBrainz ID 在 API 层生成 URL，或使用共享来源关系。
- 继续保留一个 `release-group` 的代表 release，代表选择规则不变：Official、有效曲目、曲目数、发行日期、稳定 ID。
- 导入记录继续直接公开，使用 `open`、`active`、`development` 状态，歌曲 `audio_url` 保持为空。

## 实施任务

### 阶段 1：Revision 空间优化

#### Task 1: 延迟目录 baseline revision

**目标：** 目录导入不再为每个歌曲和专辑写完整的初始 revision。

**验收标准：**

- [ ] `catalog_sync` 导入歌曲和专辑时不调用逐条 `EnsureInitialRevision`。
- [ ] `CreateRevision` 在没有 baseline 时仍会先创建兼容 baseline，再创建用户编辑 revision。
- [ ] `CreateCurrentSnapshotRevision` 及其调用路径在没有 current revision 时不会产生不可恢复错误。
- [ ] 普通手动创建、音频替换、专辑转换和修订 API 的既有行为不变。
- [ ] 样本导入后 revision 数量不再随每首目录歌曲线性增长。

**验证：**

- [ ] 增加 revision service 的 lazy-baseline、首次编辑和连续编辑测试。
- [ ] 增加 catalog sync 测试，断言导入歌曲没有 revision，首次编辑后出现合法 revision 链。
- [ ] 运行 `go test ./internal/service ./internal/modules/music`。

**可能涉及文件：**

- `internal/modules/music/catalog_sync_import.go`
- `internal/service/revision_service.go`
- `internal/service/revision_service_test.go`
- `internal/modules/music/catalog_sync_test.go`

### 阶段 2：缩减 staging

#### Task 2: 生成紧凑的代表发行中间数据

**目标：** 在生产 PostgreSQL 中不保存完整 MusicBrainz track/recording 数据集。

**验收标准：**

- [ ] 代表 release 选择结果与旧实现一致，包含无日期、无曲目、Promotion、Single、Broadcast、Other 和多碟发行测试。
- [ ] 只保留代表 release、必要 track 字段、必要 recording ID 和 artist credit 映射。
- [ ] 非代表 release 的 track 和不参与 Atoman 创建的 recording 数据在正式导入前不再占用生产 staging 空间。
- [ ] 中间数据生成过程可恢复，不需要将数百万行完整数据加载进 Go 内存。
- [ ] staging 表或中间文件的实测峰值明显低于旧方案约 21GB。

**实现顺序：**

1. 从官方 TSV 流式生成代表 release 及必要 track 字段的紧凑中间数据。
2. 保留 artist 和 credit 映射所需的最小表或压缩文件。
3. 在 Atoman 数据库中只导入紧凑 staging，并在验证后删除 staging。
4. 旧的完整 recording/track staging 仅作为过渡兼容路径，不得用于正式全量重跑。

**验证：**

- [ ] 对 fixture 和代表性样本比较新旧代表 release、track 数量和 MusicBrainz ID。
- [ ] 记录中间数据、staging、WAL 和临时排序文件的峰值。
- [ ] 运行 `go test ./internal/modules/music` 和 `go build ./...`。

**可能涉及文件：**

- `internal/modules/music/catalog_sync.go`
- `internal/modules/music/catalog_sync_import.go`
- `internal/modules/music/catalog_dump.go`
- 新增紧凑中间数据生成器及测试

#### Task 3: 去重来源数据和 staging 索引

**目标：** 在不影响 API 来源展示和查询性能的情况下移除重复数据。

**验收标准：**

- [ ] API 仍能从 MusicBrainz ID 返回对应 release-group、release、track 和 recording URL。
- [ ] 新导入歌曲和专辑不重复保存相同的来源 JSON，或来源 JSON 已迁移到共享关系。
- [ ] 只保留代表 release 选择、credit 查询和歌曲创建必需的索引。
- [ ] 不修改现有记录的 `sources_json`，不删除用户已有来源。

**验证：**

- [ ] 增加来源 DTO/API 回归测试。
- [ ] 比较优化前后的行大小、索引大小和查询计划。
- [ ] 运行音乐模块测试、Swagger 测试和 `git diff --check`。

### 阶段 3：样本容量门禁

#### Task 4: 完成代表性样本导入

**目标：** 在生产全量前用可测量样本验证空间和功能。

**样本必须包含：**

- [ ] 艺术家 credit、多艺术家和 join phrase。
- [ ] 多碟专辑、Single、EP、Broadcast、Other 和未知 primary type。
- [ ] 无日期、只有年份、只有年月和完整日期。
- [ ] duplicate release-group 和 Official/Promotion 竞争 release。
- [ ] 已存在的 Atoman 艺术家、专辑和歌曲。
- [ ] 无音频歌曲、空标题边界和 data track。

**验收标准：**

- [ ] 现有 Atoman 数据标题、类型、状态、音频地址不变。
- [ ] 每个 release-group 最多一个新专辑。
- [ ] 所有新记录公开且 `audio_url = ''`。
- [ ] 记录每张表及索引的大小、WAL 增长、临时文件峰值和导入吞吐。
- [ ] 按样本实际行大小重新计算全量上限。

**容量门槛：**

- [ ] 预测峰值低于文件系统容量，并至少保留 30GB 可用空间。
- [ ] 任一阶段剩余空间低于 30GB 时立即停止，不等待数据库报错。
- [ ] 若 staging 仍需要超过 10GB，优先改为独立压缩中间数据或扩容，不直接运行全量。

### 阶段 4：正式生产重跑

#### Task 5: 备份、执行和验证

**前置条件：**

- [ ] 新建并验证生产 custom-format PostgreSQL 备份。
- [ ] 确认 PostgreSQL 容器 healthy，backend 和 worker 使用 `127.0.0.1`、`sslmode=disable`。
- [ ] 确认官方归档 SHA-256、同步二进制 checksum 和数据库迁移版本。
- [ ] 使用新 job ID，不复用已取消 job 的状态或临时表。
- [ ] 确认正式启动前磁盘余量满足样本容量门禁。

**执行与验证：**

- [ ] 以 detached binary 启动同步，HTTP 请求不等待导入。
- [ ] 监控 job status、phase、checkpoint、正式表数量、数据库大小、WAL 和磁盘余量。
- [ ] 验证 release-group 去重、艺术家/专辑/歌曲关联、公开状态和空音频地址。
- [ ] 验证原有 15/51/745 条记录仍然存在且字段未改变。
- [ ] 完成后删除紧凑 staging 和中间文件，再执行最终 `VACUUM (ANALYZE)`，不默认使用高峰空间更大的 `VACUUM FULL`。

## 检查点

### Checkpoint A：Revision 优化完成

- [ ] 聚焦测试、修订 API 测试和音乐导入测试通过。
- [ ] 新导入样本没有逐歌曲 baseline revision。
- [ ] 首次编辑和音频替换仍能创建或更新合法 revision。

### Checkpoint B：Staging 优化完成

- [ ] 代表 release 和 track 结果与旧实现一致。
- [ ] staging 峰值、WAL 和临时文件都有实测记录。
- [ ] `go test ./internal/modules/music`、`go build ./...` 和 `go vet ./...` 通过。

### Checkpoint C：生产重跑完成

- [ ] 备份和恢复信息已记录。
- [ ] 全量导入完成且容量门禁未触发。
- [ ] 既有数据、去重结果、公开状态、来源 ID 和关联关系验证通过。
- [ ] 同步源文件、中间文件和 staging 清理完成。

## 风险与处理

| 风险 | 影响 | 处理 |
| --- | --- | --- |
| 延迟 revision 导致旧调用路径找不到 current revision | 高 | 在 `CreateCurrentSnapshotRevision` 及所有调用方增加 baseline-on-demand 测试和兼容处理。 |
| 紧凑中间数据改变代表 release 选择结果 | 高 | 对全量样本和边界 fixture 做新旧结果对比，按 MusicBrainz ID 校验。 |
| staging 仍超过磁盘余量 | 高 | 30GB 余量硬门禁；切换独立压缩中间数据或扩容，不强行继续。 |
| 来源 JSON 去重影响旧 API | 中 | 保留 ID 字段，先增加 API 回归测试，再迁移新记录。 |
| 导入中断导致部分 batch 已提交 | 中 | 每批 checkpoint 与数据事务原子提交，继续使用 insert-only 和幂等判断。 |
| 恢复或重启再次丢失连接级 staging | 高 | 正式导入前不依赖连接级完整 staging；中间数据必须可重新生成或独立保存。 |

## 暂停与恢复规则

- 在优化和样本验证完成前，不启动新的生产全量 job。
- 恢复生产导入只能使用新 job ID 和通过容量门禁的同步二进制。
- 暂停时优先保留可独立恢复的紧凑中间数据；不得把连接级临时表当作唯一恢复点。
- 任何时候磁盘剩余低于 30GB，先停止同步，再处理清理或扩容。
