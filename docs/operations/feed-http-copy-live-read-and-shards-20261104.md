# Feed COPY 发布中 HTTP 读取与回归分片验证（2026-11-04）

## 状态
隔离分支 `test/feed-rebuild-http-20261104`，基于 `4345c1d`。仅新增测试和诊断脚本。**NO-GO 生产**；不合并、不推送、不部署，不改变数据库模式或 Worker。

## 复查全量回归超时
先前 `go test ./internal/modules/feed -count=1 -timeout=480s` 在高负载共享服务器上触发 480 秒全套超时；堆栈停在 `TestRecordReadEventHandlerPersistsSourceReadEvent` 的隔离 schema 迁移过程中。此测试单独执行约 16 秒通过，不能由超时堆栈推断它本身存在死锁。模块包含约 314 个 `Test*` 函数，现无完整回归通过证据。

`scripts/test-feed-regression-shards.sh list 25` 为纯枚举，验证产生 13 个分片（前 12 个各 25，最后 14）。`run` 模式需显式设置 `FEED_SCALE_ISOLATED=yes`，每片 140 秒上限，并可指定第三参数单独运行一个分片。底层 `testdb` 仍强制测试数据库名和独立 `test_<uuid>` schema。工具是诊断用，不是将未执行分片标为通过。

示例：
```sh
./scripts/test-feed-regression-shards.sh list 25
FEED_SCALE_ISOLATED=yes ./scripts/test-feed-regression-shards.sh run 25 1
```

## COPY 真实提交窗口
`TestRSSRecentHTTPAtomicDuringRealCOPYPublish` 先发布一个有效代次，再在第二代 COPY 写完代表项与成员、仍未更新 head 时暂停事务。验证未提交数据对外不可见、HTTP 返回完整旧代，释放提交后 head 指向新代，HTTP 读取完整新代。它和既有 `TestSnapshotCopyConcurrentReaderBeforeCommit`、`TestRSSRecentHTTPRealParityFallbackAndConcurrency` 合起来补足了发布中与发布后的观测。

本轮专项回归通过，仍不代表跨节点、长周期、生产高并发和 120 秒 Worker p99 已达到上线标准。
