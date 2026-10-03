# 跨仓库后续开发计划

## 调研基线

范围覆盖 `Atoman-Backend`、`Atoman-Frontend` 和 `Atoman-Mirror` 的当前 `main`。三套代码均在独立任务 worktree 中完成 CodeGraph 索引；本计划只把已存在的路线图、源码证据和可复现检查结果整理成可执行顺序。

优先级含义：P0 会阻断发布、造成数据/权限错误或破坏核心契约；P1 会持续造成用户可见错误或明显运维风险；P2 是可在核心闭环稳定后推进的能力和质量工作。

## P0：先修正确性与发布链路

### 1. 统一 Mirror 的部署入口

- 证据：新版 Go/Vue 入口注册 `/`, `/images`, `/search` 和 `/api/*`（`Atoman-Mirror/src/main.go:63-113`、`src/handlers/imagetar.go:709-715`、`src/handlers/search.go:469-500`）；现有 Nginx 只静态返回旧 `/index.html`，其余路径直接 404（`Atoman-Mirror/nginx/mirror.atoman.org.conf:305-319`）。
- 工作：选择“Go embed 单入口”或“Nginx 静态站点 + API 反代”中的一个正式拓扑，删除另一套过期配置；把 `/api`, `/images`, `/search`, `/assets/*`、SPA 回退和 `/ready` 写成部署契约。
- 验收：在 clean checkout 按发布脚本构建后，Go 服务和 Nginx 配置分别能返回 `GET /`、`GET /images`、`GET /search` 为 `200`，`GET /api/search?q=nginx` 能到达后端；加入反代 smoke test。
- 依赖：先决定生产入口，再更新 README、Nginx 和 release workflow。

### 2. 修复 Feed 已读与暂停订阅语义

- 证据：博客 Feed 对 `is_read=true` 直接返回空（`Atoman-Backend/internal/modules/feed/service_timeline.go:250-259`），对 `false` 没有过滤；同一分支重新读取全部订阅且不跳过 `IsPaused`（`.../service_timeline.go:260-281`），而普通分支已在 `:88-91` 跳过暂停订阅。
- 工作：让博客、RSS、视频和播客共用一套 `IsRead`/暂停订阅过滤；补充 `content_type=blog` 的 true、false、paused 三组回归测试。
- 验收：已读查询只返回已读项，未读查询只返回未读项，暂停内部订阅不会产生文章；总数与列表一致。

### 3. 收紧 Mirror 认证边界

- 证据：token 缓存键只取 `RawQuery`（`Atoman-Mirror/src/handlers/docker.go:309-328`），但上游请求会转发全部请求头（`:351-371`）；realm 改写固定为 `http://`（`:438-450`）。
- 工作：认证请求带 `Authorization` 时禁用共享缓存，或把可信身份、路径、registry、service、scope 纳入键；依据可信代理的 `X-Forwarded-Proto/Host` 生成 realm。
- 验收：不同 registry、scope、认证身份不会命中同一缓存；HTTPS 反代下 `Www-Authenticate` 为 HTTPS；增加单元和反代集成测试。

### 4. 把发布检查变成可重复的 CI 门槛

- 后端：保留 `go build ./...`，并为 Feed 语义和导入清理增加定向测试。
- 前端：当前 CI 只有类型检查、构建和单元测试（`Atoman-Frontend/.github/workflows/ci.yml:67-115`），补充登录、Feed、Studio、音乐导入的 Playwright 冒烟。
- Mirror：先生成 `src/dist` 再执行 `go test ./...`；同时运行 `web` 的 `npm run build` 与 `npm run test:ui`。直接在未生成产物的 checkout 执行 Go 测试应给出明确提示，而不是 `pattern all:dist`。

## P1：完成核心路线图并降低运行风险

### 5. 完成 Feed 搜索质量闭环

- 后端现有查询仍使用 `LOWER(...) LIKE '%term%'`（`Atoman-Backend/internal/modules/feed/repo.go:411-416`、`:1073-1089`），已有 B-tree 查询索引迁移但没有可量化的相关性/延迟基线。
- 按现有路线图定义查询解析、排序、高亮、分页和新鲜度；基于真实数据做 `EXPLAIN`，再决定 `pg_trgm`/全文索引和 ranking；前端同步空结果、排序和恢复状态。
- 验收：记录 p50/p95 延迟、相关性样例和分页稳定性，避免先暴露无测量的高级排序。

### 6. 统一媒体异步生命周期

- 后端导入 worker、上传会话和前端导入/播放器已有恢复逻辑，但失败清理、重试、轮询和可观测性分散。
- 先定义上传、处理、提交、取消、重试的状态转移和幂等键，再让音乐、播客、视频复用同一状态展示与退避策略。
- 验收：刷新、断网、重复提交、对象存储失败均有可恢复状态和明确错误原因。

### 7. 修正 Studio Overlay 契约

- `StudioLayout` 只在 scoped 样式设置侧栏变量（`Atoman-Frontend/src/views/studio/StudioLayout.vue:71-74`），`StudioRouteSheet` 未传 `above-player`（`.../components/studio/StudioRouteSheet.vue:2-11`）；Teleport 后的 `PSheet` 依赖 root 变量（`.../components/ui/PSheet.vue:37-43,470-520`）。
- 统一传递侧栏边界和层级，补桌面侧栏点击、播放器覆盖、移动底部 Sheet 的契约测试。

### 8. 同步路线图、文档和安装流程

- 前端 `ROADMAP.md` 的 Feed 规则仍全部未勾选（`Atoman-Frontend/ROADMAP.md:3-19`），但实现已存在；Mirror README 引用不存在的 `packaging/hubproxy.service`（`Atoman-Mirror/README.md:42-48`），实际文件为 `atoman-mirror.service`。
- 每个已交付项记录状态、证据和下一步；发布前对 README、Nginx、systemd、API 文档做一次 clean-install 校验。

## P2：稳定后再扩张

- 按两套产品路线图推进公共 API、浏览器扩展、移动端同步、Newsletter/YouTube 等外部来源；前置条件是鉴权、限流、审计、版本和隐私契约稳定。
- 建立版本、路由、API 失败率、导入完成率和测试不稳定率的月度复盘，删除没有用户或运营指标的工作项。

## 建议执行顺序

1. Mirror 部署入口与认证安全。
2. 后端 Feed 已读/暂停语义及回归测试。
3. 三仓库 clean-build/CI 门槛。
4. Studio Overlay 和媒体状态契约。
5. Feed 搜索基线与索引优化。
6. 文档、路线图和安装流程同步。

## 基线检查结果

| 仓库 | 检查 | 结果 |
| --- | --- | --- |
| Backend | `go build ./...` | 通过 |
| Backend | `go test ./internal/modules/feed` | 因未配置 `TEST_POSTGRES_DSN` 在测试数据库初始化阶段退出 |
| Frontend | `bun run type-check` | 任务 worktree 没有未跟踪的 `node_modules`，命令无法启动 `vue-tsc` |
| Mirror | `npm run test:ui --prefix web` | 4/4 通过 |
| Mirror | 先 `web` build 再 `cd src && go test ./...` | clean checkout 流程通过；直接 Go 测试会因未生成 `src/dist` 失败 |
