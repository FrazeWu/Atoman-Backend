# 跨仓库重构与优化清单

原则：先抽稳定契约，再拆实现；每次只移动一个职责，保持 API/路由不变，配套定向测试和性能基线。以下规模数字来自当前 `main`，不是估算。

## 后端

### R1. 抽取统一 Timeline Builder（P1）

- `Atoman-Backend/internal/modules/feed/service_timeline.go:130-247` 与 `internal/modules/feed/subscription_hub.go:919-1011` 都重复加载 posts、videos、feed items、read map、DTO、过滤和排序。
- 将流程拆成 `SourceSet -> ContentSnapshot -> TimelineItemDTO -> Filter/Sort/Page` 四个小职责；博客专用分支只提供来源集合，不再重新解释订阅状态。
- 收益：一次修复 `is_read`、暂停订阅和事件语义漂移；减少重复查询和不同入口的分页差异。
- 守护：先保留旧入口，使用同一组 fixture 比较旧/新 DTO 和总数，再删除重复代码。

### R2. 统一 Feed 查询条件与搜索谓词（P1）

- `internal/modules/feed/repo.go` 同时维护 `buildFeedItemsBySourceIDsQuery`（`:509-539`）和 `buildExploreFeedItemsQuery`（`:1050-1092`），搜索、可见性、已读条件容易漂移。
- 抽共享的可见性、语言、分类、已读和搜索 predicate builder；把 `%term%` 查询封装成可替换的 PostgreSQL strategy。
- 先用 `EXPLAIN (ANALYZE, BUFFERS)` 建立 p50/p95 与扫描行数基线，再选择 trigram/tsvector、排序字段和索引。

### R3. 拆分音乐导入服务边界（P1）

- 生产文件规模较大：`internal/modules/music/import_metadata_enricher.go` 2084 行、`media_import_processor.go` 1775 行、`import_commit_service.go` 1580 行、`import_worker.go` 759 行。
- 按上传注册、对象存储、元数据匹配、处理 worker、提交/清理拆成端口明确的包；清理函数返回值必须由调用者处理或转入可靠的 outbox/重试队列。
- 先以接口包和表格化状态转移为边界，避免一次性重命名模型或迁移数据库。

### R4. 统一生命周期事件读取策略（P1）

- `subscriptionContentReadMap`（`internal/modules/feed/subscription_hub.go:1014-1041`）与普通 Feed 的 read map 各自查询，且事件语义不一致。
- 建立一个 `ReadPolicy`，明确 `open`、`engaged`、`complete` 是否算已读；查询函数只返回策略定义的事件集合。
- 为策略写表驱动测试，避免把 like/comment/bookmark 等互动事件误当阅读事件。

## 前端

### R5. 拆分 Feed 来源管理（P1）

- `Atoman-Frontend/src/components/feed/SubscriptionManageSheet.vue` 2216 行，同时承载 OPML、健康检查、同步、分组、来源、批量操作、规则和关键词。
- 按“来源列表/分组/批量操作/健康诊断/规则编辑/导入导出”拆成子组件和 composable；容器只保留 sheet 状态与路由协调。
- 每次拆分保留现有事件名和 Pinia store，先迁移单测再调整 UI。

### R6. 拆分推荐页与播放器状态（P1）

- `FeedRecommendedView.vue` 1579 行混合筛选、分页、订阅和卡片展示；`stores/player.ts` 1389 行混合音频状态、队列、预加载、媒体会话和 API 请求。
- 推荐页抽 `useRecommendationQuery`、`useSubscriptionActions` 和纯展示卡片；播放器按 playback、queue、prefetch、media-session 四个 slice 拆分，统一取消过期请求。
- 以现有播放器/推荐测试作为行为快照，先抽纯函数和状态模块，不改变公共 store 名称。

### R7. 收敛路由 Overlay 与 API transport（P1）

- `PSheet` 已承担 Teleport、层级、焦点和侧栏边界；业务组件仍可能自行决定变量或层级。将 Studio、Blog、媒体编辑器的调用参数收敛到统一 adapter。
- API 请求统一错误 envelope、AbortSignal、重试和 session 失效处理，避免页面直接拼接 URL 与重复 headers。
- 增加路由刷新、快速切换、关闭历史和 401/超时的契约测试。

## Mirror

### R8. 重构配置与缓存并发模型（P1）

- `Atoman-Mirror/src/config/config.go:150-198` 同时维护两把锁，读取和设置顺序相反；`src/handlers/search.go:99-134` 是手写 TTL map，容量达到上限时没有淘汰未过期条目。
- 用不可变配置快照替代双锁；缓存抽成带原子过期检查和明确容量策略的 LRU/分片缓存，区分公开搜索缓存与认证 token 缓存。
- 加 `-race`、并发读写、过期替换和容量上限测试。

### R9. 拆分 Mirror handler 与上游客户端（P2）

- `src/handlers/imagetar.go` 同时负责 token、防抖、tar、平台选择、流式下载和响应错误；`search.go` 同时负责缓存、分页、Docker Hub HTTP 和 JSON 规范化。
- 提取 `upstream/client`、`cache`、`tar/manifest`、`httpapi` 四个边界；所有上游请求接收 context，重试使用可取消 timer。
- 保持 Gin 路由不变，以现有 handler 测试和 UI 合约测试作为迁移护栏。

### R10. 把构建产物变成显式契约（P0/P1）

- `src/main.go` 使用 `//go:embed all:dist`；产物由 `web/vite.config.ts` 输出到 `../src/dist`，只有按特定顺序执行 release 脚本才完整。
- 在 `scripts/build-go.sh`、Makefile 和 CI 中统一一个 `build-web-and-go` 入口；单独 `go test` 提供明确的生成步骤或把 embed 与核心包测试分离。
- 目标是开发者、Docker、release workflow 使用同一产物路径，避免“本地通过/clean checkout 失败”。

## 推荐重构顺序

1. 先定义跨端契约：Feed read policy、媒体状态、Mirror 部署入口。
2. 抽后端共享 builder 和 Mirror 配置/缓存基础设施。
3. 拆前端 Feed 管理和播放器，保持 store/API 兼容。
4. 最后拆大 handler 和删除旧兼容层；每一步都运行受影响测试、类型检查或构建。
