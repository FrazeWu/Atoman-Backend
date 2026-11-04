# Atoman Feed COPY: 接口契约、多代 GC、行锁压力与 WAL 验证（2026-11-04）

## 边界

- 本文来自 `test/feed-copy-api-gc-20261104`（基于 `test/feed-copy-preflight-hardening-20261104` 的独立 worktree），**仅对一次性隔离 PostgreSQL 16 测试库和合成 Feed 数据运行**。生产 `main`、API 路由、Worker、数据库模式、DNS、Cloudflare 均未修改；不合并、不推送、不部署。
- 现有 COPY 写入必须同时具备 `FEED_SCALE_ISOLATED=yes` 和 `FEED_SNAPSHOT_COPY_EXPERIMENT=yes`，且数据库名属于 `_test` 和 schema 为 `test_<32 hex>`；正式 HTTP 接口并未使用 COPY 读路径。测试用开关严禁直接转换为生产发布开关。

## A. 公开 Feed / Explore 请求级差异测试

- 隔离合成样本：**28 条文章，24 个去重代表项**，两个 RSS 来源，含重复 URL、中文/emoji 标题、正文及 reader HTML。启用 COPY 生成快照并通过 revision 追踪读取。
- 以既有生产 Service 调用 `GetPublicFeed(FeedQuery)` 和 `GetExploreFeed(anonymous, FeedQuery)` 为旧 API 输出的基准，针对 1/2/3/5/10/20/50 个 page size、多个有效页和空尾页，和 `readPublicRecentDedupSnapshotPageTracked` 比较。**65 组页码+分页大小组合**覆盖总数、顺序、条目 ID、链接、标题、发布日期、正文、reader HTML、去重数量与成员、来源 ID、guest read flag；全数匹配。
- 对不受支持的筛选作**反例验证**：有 `Search`、`SourceID`、`IsRead=true` 的实际 Service 返回值是搜索子集、单来源子集或无已读条目，而全局 RSS 快照没有编码这些条件；因此这些请求不可直接映射到 global projection。含博客/播客/视频的混合时间线、订阅及权限、热门/随机排序，也**尚未**获得等价验证；不能直接接入。
- 在快照发布后改变来源文章标题触发 revision 变化，`readPublicRecentDedupPageWithFallback` 检测到 stale generation，执行原有 Service 查询并成功返回内容。**该测试对照的是实际 Service 方法，而不是启动真实 HTTP 服务进行端到端浏览器请求。**

## B. 七代快照、长读事务与 GC/WAL

- `TestSnapshotCopyMultiGenerationGCAndWAL`：5,000 条无生产数据的合成文章；先 GORM 生成一代，再 COPY 连续发布六代。在第 4 代上固定 PostgreSQL `REPEATABLE READ` 旧事务，继续发布到第 7 代；将旧代 `built_at` 在测试库中人为调整为 2 小时前，然后以现有 1 小时 grace 调用真实 GC。
- **正确删除过期 6 代，仅保留活动代 1 代与 5,000 条成员**；GC 后原有旧事务仍能读取原快照完整分页，重试 GC 不再误删；退出码 0。
- 测量：从首代发布到 GC 结束 WAL 累计 **约 38.7 MiB**；GC 前后数据库占用均约 **42.4 MiB**，这是 PostgreSQL 的空间复用现象，不能解释成释放了磁盘容量。测试末 Go HeapAlloc 约 7.5 MiB（只是时点值，不是过程峰值）。
- 这并不是 7 小时或 7 天真实耐久性测试；GC 时间是通过人工修改测试元数据压缩出来的，也没有执行真实多日 autovacuum/backup/restore 演练。不能把这组实验外推为生产存储的长期增长率。

## C. 真实 row lock 与 context 超时

- `TestSnapshotCopyScopeHeadLockTimeoutAndRecovery`：在旧 head 上由独立事务保持 `SELECT FOR UPDATE` 行锁；在另一个连接用完整 COPY publisher 提交更新，设置约 650ms context 截止。实测约 **653ms** 返回超时错误，旧 head 及全部旧行/成员仍可读取，未留下不完整代次。解除锁后再次 COPY 发布成功。退出码 0。
- 这验证单节点真实 PostgreSQL 头指针竞争及连接池恢复，不是 120s Worker 预算、网络断连/进程强杀或多节点全链路压测。

## 当前结论：NO-GO 上线

研究用 COPY 在之前的 100k ABBA 对照中，写入阶段中位数下降约 46–62%，但是本次进一步证实 **只可在严格 RSS-only/recent/无用户筛选的请求形态**考虑未来适配。其他请求直接套用全局快照会改变数据语义。GC 的删除成功不代表磁盘立即回收，也不能证明自动清理在真实长周期内安全。

### 后续上线阻断项

1. 必须明确将来的 **读端请求形态校验 / fail-closed gate**，给混合博客、订阅、已读、搜索、分类、语言、来源、随机/热门排序配置强制旧读路径，不得仅因存在快照就绕过原路由。需要真正的 HTTP 层一致性和用户授权数据隔离验证。
2. 正式 Worker、迁移/触发器、影子写入、读特性开关与切换/回滚仍没有进入主分支。未来需要独立的、默认关闭的受控生产开关；不能直接去掉现有 `_test` 限制。
3. 需要经过授权的脱敏生产形态副本、重复大规模最坏情况及 120 秒 Worker p99 边界验证、24h+ WAL/autovacuum/锁等待/表膨胀监控、真实备份与恢复演练、异步请求取消及多 Worker 并发重试。必须由单独审批放行，不因本报告结果自动部署。
