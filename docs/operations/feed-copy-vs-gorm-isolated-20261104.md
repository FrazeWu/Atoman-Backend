# Feed 快照写入：PostgreSQL COPY 与 GORM 隔离 A/B 验证（2026-11-04）

## 范围与安全

- 工作区 `/root/Atoman/Atoman-Backend/.worktrees/feed-copy-benchmark-20261104`，分支 `perf/feed-copy-benchmark-20261104`，基于 `8f8f9fe`。本次仅增加 Go **测试文件**、资源受限的隔离测试脚本及本报告，**没有将 COPY 接入生产代码或 worker**；不修改 `main`，不推送、合并、部署，不触碰生产数据库、NAS 或实际用户 Feed。
- 执行 `scripts/test-feed-copy-isolated.sh 10000|100000|360000`，禁止继承 `TEST_POSTGRES_DSN`、`TEST_DATABASE_URL`；每次用唯一临时 PostgreSQL 16 容器与 loopback-only 端口，数据库名 `atoman_scale_test`，另为每项测试创建独立随机 schema。容器限制 1.5 CPU、1.5 GiB、不允许交换；Go 限制 1.5 CPU、2.2 GB、不允许交换；14 分钟外部超时，测试结束按容器 ID 清理。只生成合成文章、标题、链接和 UUID，不导出任何真实数据。
- 测量对象是**已在内存中组装好的** `feed_dedup_snapshot_rows` 与 `feed_dedup_snapshot_members` 的写入过程，加上同一事务内的 generation 记录及 head 原子更新。GORM 走现行默认 350/300 分批 `Create`；COPY 使用现有 pgx/v5 stdlib 连接的 `sql.Conn.Raw` + 单一 `pgx.Tx` + 两次 `CopyFrom` + head UPSERT + COMMIT。两者写入同一 PostgreSQL schema，但使用不同 generation ID。测量**不包括**真实 Feed 拉取、去重、JSON 组装、HTTP 查询、Worker 调度或生产网络耗时。
- Go `TotalAlloc` 是**累计分配**，不是峰值。`VmHWM` 是整个测试进程的累积 RSS 高水位（第二种方法包含第一种运行过的峰值，不能拆分为独立峰值）；WAL 使用隔离 DB 的 `pg_current_wal_insert_lsn()` 差值，属于估算而不是生产 WAL 保证值。

## 时间及资源对照（单次 A/B，执行顺序交叉）

| 样本量 / 顺序 | GORM 总写入 | COPY 总写入 | GORM/COPY 加速比 | GORM Go 累计分配 | COPY Go 累计分配 | GORM WAL | COPY WAL |
|---|---:|---:|---:|---:|---:|---:|---:|
| 10k，GORM 先 | 0.734 s | 0.245 s | 3.00x | 90.2 MiB | 25.8 MiB | 10.70 MB | 9.80 MB |
| 10k，GORM 先，加入并发测试重跑 | 0.524 s | 0.173 s | 3.03x | 90.2 MiB | 25.8 MiB | 10.70 MB | 10.07 MB |
| 100k，GORM 先 | 5.680 s | 2.463 s | 2.31x | 900.6 MiB | 252.3 MiB | 107.66 MB | 101.10 MB |
| 100k，COPY 先 | 5.665 s | 2.537 s | 2.23x | 900.6 MiB | 252.4 MiB | 107.76 MB | 98.25 MB |
| 360k，GORM 先 | 21.080 s | 10.874 s | 1.94x | 3,241.7 MiB | 906.6 MiB | 390.89 MB | 367.51 MB |
| 360k，COPY 先 | 23.104 s | 8.544 s | 2.70x | 3,241.6 MiB | 906.6 MiB | 406.10 MB | 355.78 MB |

- 10k 对应 **9,167 代表项 + 10,000 成员**；100k 对应 **91,667 代表项 + 100,000 成员**；360k 对应 **330,001 代表项 + 360,000 成员**。所有 A/B 生成的内容字段完全相同。
- 360k 的代表项插入 GORM 为 8.75 / 8.39 s，COPY 为 2.24 / 2.30 s；成员插入 GORM 为 12.33 / 14.71 s，COPY 为 8.62 / 6.24 s。因此收益主要来自避免 ORM 的多次多行 INSERT 构造，尤其是代表项。
- 360k 首次运行报告的进程 VmHWM：GORM-first 约 **362,348 KiB**，COPY-first 约 **369,476 KiB**。这是**合成写入测试**的进程累计峰值，非实际 Feed 全量重建约 1.2 GiB 的内存需求。测试 DB 容器终态显示约 154–168 MiB，未声称连续监控的 DB 峰值。
- 两种顺序的 100k/360k 单次实验均显示 COPY 优势，但由于 VPS CPU/缓存/磁盘波动，不能据此给出正式线上吞吐 SLA 或精确放大比例。

## 结果正确性、失败与锁竞争

1. **完整字段对比**：两个 generation 的代表项使用 position 全外连接，核对 ID、发布时间、规范化 URL、标题、重复数、JSONB 成员 IDs 和来源；成员使用 item ID 全外连接，核对发布时间、URL、URL hash、标题，并核对各表条目数。10k/100k/360k 均为零差异。
2. **故障回滚**：在同一 COPY 事务中，先插入 generation 和代表项，再向成员表提交重复复合主键，验证唯一约束错误导致整个事务回滚，generation/representatives/members 均不泄漏、已有 head 指针不变。
3. **MVCC 原子发布**：独立 240 条微型并发场景，Writer A 完成两个 COPY 且执行 scope head UPSERT 后暂停未提交，普通 reader 仍看到上一个已提交 generation，看不到 Writer A 的新数据。
4. **两个并发写入**：Writer B 在另一连接完成 COPY 后尝试对同一 scope head UPSERT，被 Writer A 持有的行锁阻塞；A COMMIT 后 B COMMIT，最终 head 指向 B，旧、A、B 三个 generation 的成员均完整，不出现半成品。并发测试没有证明生产 worker 的 revision 新旧顺序守卫；后续必须在真正发布代码中保留上游 revision 校验/互斥与取消策略。
5. **所有隔离测试正常结束并销毁测试容器**；不修改原有 snapshot publishing 逻辑。

## 下一步上线前门禁（仍然 NO-GO）

- 在新的独立工作区把 COPY **作为可关闭的非生产试验路径**加入全量重建流程时，必须保留同一事务内 generation、两个 COPY 和 head 发布、源 revision/checkpoint 验证、锁策略、工作超时和失败 fallback；严禁仅将 GORM `Create` 替换为独立连接上的裸 COPY 造成非原子状态。
- 在生产形态的独立 360k Feed 端到端测试中同时跑拉取、去重、组装、写入、读端一致性与 old generation GC，检查 CPU、RSS、WAL 峰值、磁盘膨胀、长事务、故障注入、连接池和取消。
- 验证真实生产脱敏副本及多次交叉顺序对照，确认搜索、订阅、混合博客、已读、热门/随机等仍保持旧 API 契约；完成可逆迁移、备份恢复、Worker 独立启停、灰度与回退后才可能申请生产发布。

**结论**：在隔离的持久化微基准中 COPY 的写入耗时稳定下降，值得在后续非生产全链路验证；本阶段不修改生产写入实现，更不启用新投影读路径。
