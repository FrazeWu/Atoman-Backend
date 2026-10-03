# 跨仓库 Bug 修复清单

以下条目按影响排序。每项都给出触发条件和现有证据；没有把单纯的代码味道写成 Bug。

## P0/P1：先修

### B1. 博客 Feed 的 `is_read` 过滤错误（Backend，P1）

- 位置：`Atoman-Backend/internal/modules/feed/service_timeline.go:250-259,299-333`；查询契约在 `internal/modules/feed/http.go:793-800`。
- 触发：请求 `/api/v1/feed/timeline?content_type=blog&is_read=true` 永远返回空；传 `is_read=false` 时博客文章仍混合返回已读和未读。代码只在 `true` 时短路，之后只给 DTO 标记 `IsRead`，没有过滤。
- 影响：客户端的未读筛选、未读计数和分页语义错误。
- 修复/测试：让仓储查询或统一 timeline filter 真正按 read map 过滤；补 true/false/无参数和总数一致性测试。现有测试覆盖 RSS 已读场景，但博客测试没有该场景。

### B2. 暂停的内部订阅仍出现在博客 Feed（Backend，P1）

- 位置：普通分支在 `internal/modules/feed/service_timeline.go:88-91` 跳过 `IsPaused`，但博客分支在 `:260-281` 重新加载全部订阅并追加用户/频道 ID。
- 触发：暂停一个 `internal_user` 或 `internal_channel` 订阅，再请求 `content_type=blog`；其文章仍会进入 `ListSubscribedBlogPosts`（`internal/modules/feed/repo.go:361-434`）。
- 影响：用户暂停订阅后仍被推送内容，且不同 Feed 入口行为不一致。
- 修复/测试：复用已过滤的 source set，或在博客分支显式跳过 `IsPaused`；补内部用户和频道两种回归测试。现有暂停测试只覆盖 external RSS。

### B3. 互动事件被误当作“已读”（Backend，P1）

- 位置：`internal/modules/feed/subscription_hub.go:1014-1041` 的 `subscriptionContentReadMap` 只按 user/content 分组，没有限定 `event`；生命周期允许 `impression/open/engaged/complete/like/comment/bookmark/share/follow`（`internal/modules/lifecycle/service.go:104-107`）。
- 触发：用户只点赞、评论、收藏或产生曝光事件，没有打开文章；查询 Feed 时仍会被标记 `is_read=true`。真正的订阅已读写入 `event="open"`（`internal/modules/feed/service_engagement.go:239-255`）。
- 影响：后续未读筛选、阅读统计和 Today inbox 都会少展示内容。
- 修复/测试：明确“已读事件集合”（至少 `open`，或产品确认的 `open/engaged/complete`），在 SQL 中限定事件；增加非 open 事件回归测试。

### B4. Mirror token 缓存可能跨请求泄露认证响应（Mirror，P1）

- 位置：`Atoman-Mirror/src/handlers/docker.go:309-328` 的键只包含 `RawQuery`；同文件 `:351-371` 转发全部请求头。
- 触发：启用 token cache 时，两个不同路径/registry/Authorization 身份使用相同 query；后一个请求可命中前一个响应。
- 影响：错误的 registry token 或带认证信息的响应被共享，属于权限边界问题。
- 修复/测试：有 `Authorization` 时禁用共享缓存，或将路径、registry、service、scope 和可信身份摘要纳入 key；增加跨 registry/身份隔离测试。

### B5. Mirror HTTPS 反代返回 HTTP realm（Mirror，P1）

- 位置：`Atoman-Mirror/src/handlers/docker.go:380-396,438-450` 只取 `Request.Host`，`rewriteAuthHeader` 固定拼接 `http://`；Nginx 已发送 `X-Forwarded-Host`/`X-Forwarded-Proto`（`nginx/mirror.atoman.org.conf:52-59`）。
- 触发：Docker 客户端通过 HTTPS Nginx 访问 registry，收到 `Www-Authenticate: Bearer realm="http://.../token"`。
- 影响：客户端可能降级请求、拒绝 token 或在严格 TLS 环境失败。现有 `src/handlers/docker_test.go:138-152` 反而把 HTTP realm 固定为测试契约。
- 修复/测试：只信任配置的反代头并保留 scheme/host；补 HTTPS 反代测试，更新旧 HTTP 期望。

### B6. Mirror Nginx 与新版 Go/Vue 路由不一致（Mirror，P0）

- 位置：Go 注册 `/api/*` 和 SPA 路由（`Atoman-Mirror/src/main.go:63-113`、`src/handlers/imagetar.go:709-715`、`src/handlers/search.go:469-500`）；Nginx `/` 仍服务 `/var/www/atoman-mirror/index.html`，其余未知路径直接 404（`nginx/mirror.atoman.org.conf:305-319`）。
- 触发：生产使用该 Nginx 配置时访问新版 `/images`、`/search` 或 `/api/image/*`。
- 影响：线上展示旧页面，搜索/离线镜像功能返回 404。
- 修复/测试：选定单一部署拓扑并删除旧静态规则；用 Nginx 配置测试和真实 `GET /`/`/api` smoke test 验证。

### B7. 离线 tar 的 repositories 元数据解析错误（Mirror，P1）

- 位置：`Atoman-Mirror/src/handlers/imagetar.go:521-527` 用 `strings.Split(imageRef, ":")` 判断 tag。
- 触发：`registry.example:5000/team/app:latest` 不生成 repositories；`ghcr.io/team/app@sha256:<digest>` 会把 digest 前缀误当仓库/标签。该行为已用无网络 v1.Image 测试复现。
- 影响：Docker load 后 tag/repository 映射缺失或错误，离线包不可用。
- 修复/测试：用 `name.ParseReference` 区分 Tag/Digest、registry port 和默认 namespace；补端口、digest、普通 tag 三组 tar 测试。

## P1/P2：可靠性与维护风险

### B8. Mirror 配置锁顺序反转可能死锁（Mirror，P1）

- 位置：`Atoman-Mirror/src/config/config.go:160-181` 先持有 `configCacheMutex` 再读 `appConfigLock`；`setConfig` `:189-197` 反向持有。
- 触发：配置缓存过期时并发调用 `GetConfig` 与 `LoadConfig/setConfig`。
- 影响：请求线程和配置刷新线程可能互相等待，服务无响应。
- 修复/测试：统一锁顺序或改为原子不可变快照；增加并发测试并运行 `go test -race ./config`。

### B9. Mirror 搜索分页缓存串页且上游请求不可取消（Mirror，P1）

- 位置：标签缓存键 `src/handlers/search.go:333-368` 不含 `pageSize`；搜索/标签分页解析 `:439-454` 不限制上限；`fetchTagPage` `:381-386` 忽略 context。
- 触发：同一仓库先请求不同 `page_size`，或客户端断开连接/重试等待期间；缓存可能返回错误数量，上游请求仍继续运行。
- 影响：页面显示错误、可被大参数放大上游和内存消耗。
- 修复/测试：键包含所有分页参数，统一 page/pageSize 上限；用 `NewRequestWithContext` 和可取消 timer；补缓存隔离与取消测试。

### B10. 导入对象清理失败被静默吞掉（Backend，P2）

- 位置：`Atoman-Backend/internal/modules/music/import_upload_lifecycle_service.go:188-192,227`、`import_upload_replace_service.go:110-115`、`import_multipart_service.go:153-161` 调用 `cleanupAlbumImportObject` 但忽略返回值。
- 影响：对象存储删除/中止失败时，数据库记录可能已删除，清理目标记录失败也不会反馈，长期积累孤儿对象。
- 修复/测试：将清理结果写入可靠重试队列或返回可观测错误；补删除、替换、取消三条失败路径的测试和指标。

### B11. Mirror 安装文档引用不存在的服务文件（Mirror，P2）

- 位置：`Atoman-Mirror/README.md:42-48` 引用 `packaging/hubproxy.service`，仓库实际只有 `packaging/atoman-mirror.service`。
- 影响：按文档执行安装会直接失败。
- 修复/测试：统一服务名、包脚本和 README；增加 clean-install smoke test。

## 已验证检查

- `Atoman-Backend`: `go build ./...` 通过；Feed 定向测试因当前环境未设置 `TEST_POSTGRES_DSN` 无法初始化测试数据库。
- `Atoman-Frontend`: 任务 worktree 缺少未跟踪依赖，`bun run type-check` 无法启动 `vue-tsc`；CI 配置本身未包含 E2E。
- `Atoman-Mirror`: `npm run test:ui --prefix web` 4/4 通过；先构建 `web` 再运行 `cd src && go test ./...` 通过，直接 Go 测试会因未生成 `src/dist` 失败。
