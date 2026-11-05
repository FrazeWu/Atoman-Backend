# Feed RSS-only HTTP 隔离入口和快照门禁（2026-11-04）

## 适用范围与状态

此实现来自 `feat/feed-rss-http-isolated-20261104`，由 `a49bc6f` 分叉。状态：**隔离测试；NO-GO 生产**。不得把本分支测试条件当作已批准上线流程；未对生产数据库、生产 Worker、正式前端或 Cloudflare 作任何变更。

新实验入口：`GET /api/v1/feed/rss/recent`。只表示**公开、未隐藏、`source_type=external_rss`、最近优先、去重后的结果**；并非 `/api/v1/feed/explore`（混合站内文章、RSS、播客与视频），也非订阅、已读、个性化时间线。

默认入口直接 **404**，必须同时满足：
- `FEED_SCALE_ISOLATED=yes`
- `FEED_SNAPSHOT_RSS_HTTP_EXPERIMENT=yes`
- 数据库名称必须满足现有测试库约束，当前 schema 必须是 `test_<32 hex UUID>`，由现有 `snapshotCopyTargetAllowed` 检查。

只在满足上述条件后解析参数；允许 `page`、`page_size`、`hide_duplicates=true`、`sort=recent`。page 为正整数，page_size 为 1–100。缺省为第一页、每页 20、去重、recent。重复参数及任何用户已读、搜索、语言、分类、订阅分组、来源与未知条件均 **400**，绝不默默映射为全局快照。

默认执行旧查询，并保持 `TimelineItemDTO` + `httpx.List` 响应字段与 meta。只有再开启 `FEED_SNAPSHOT_READ_EXPERIMENT=yes`，且当前请求通过匿名、来源、原始参数与无筛选检查，才尝试快照读；**认证用户永远旧读**。快照尝试超时设为 500 ms（根据共享测试机 80 ms 不稳定的实际失败修正；不是生产 SLA），失败时不返回部分数据，而是同步回退原生 RSS 查询。客户端取消则不触发昂贵的旧查询。

COPY 写入仍保持独立的 `FEED_SNAPSHOT_COPY_EXPERIMENT=yes` + `FEED_SCALE_ISOLATED=yes` 和 _test/test_ 数据库硬门禁；这不是生产发布开关。

## 数据范围修正

原来的快照发布扫描 `feed_sources.hidden=false`，没有明确限制 `source_type`。现加 `source_type='external_rss'`，与独立旧查询的语义对齐。测试用可见非 RSS 来源插入同 URL 且时间更靠前的条目，确保不能影响 RSS 去重代表项、数量、顺序和 HTTP 返回。注意当前 revision 水位仍包含其他来源变动，可能导致保守的额外失效，但不会因此扩大快照结果范围。

## 验收与限制

隔离 PG16 + Gin httptest 覆盖多页大小与页码、完整 JSON DTO、总数、来源、中文与 Unicode、重复 URL；缺失/过期代次回退；默认关闭；登录用户旧读；无效查询 400；24 并发 GET。现有混合 Explore 契约回归继续保留。

实验性 `X-Feed-Isolated-Read-Path` header 仅用于隔离读取时标识 `snapshot` / `fallback`；正常 legacy 路径不返回。

仍未完成：生产可用的正式独立读/写开关、真实 HTTP 网关/浏览器集成、隔离授权副本、跨节点并发+动态重建和 24h+ GC/WAL/Autovacuum 监测、正式迁移/回滚、真实备份恢复演练、Worker p99 边界验证、正式灰度审批。即使全量单元测试通过，也不自动推进部署、合并或对外启用。
