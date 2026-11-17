# Release Notes Draft — v0.4.0

> 草稿，尚未发布。上一版说明见 [v0.3.0.md](v0.3.0.md)。
> 范围：Backend `v0.3.0`（`9fafc1a6`）..`bea8654f`，Frontend `v0.3.0`（`2bc6e920`）..`21e484f5`。
> 生产现状（2026-11-17）：后端运行 hotfix `87908bd4`（= `e5dda0e9` + Go 1.26.11）；前端 `main` 已由 Cloudflare Pages 发布。

## 中文

### 版本概览

Atoman 0.4.0 上线自建邮件服务与 Webmail、按文件管理的图书与精确续读、统一搜索、重做的移动端 Web App，以及加密备份恢复校验、不可变运行时等运维能力。后端构建要求 Go 1.26.11（修复 GO-2026-6859）。

### 主要进展

- **邮件**：基于 Stalwart + JMAP 的自建邮箱与 Webmail，包括管理员邀请、自选地址、5 秒内可撤销的持久发件队列和私有 R2 存储；通讯录、签名、高级搜索、定时发送、全站未读数；私有标签与 Snooze / 跟进提醒（仅在 Webmail 内生效，不同步到其他客户端）。外部 IMAP、历史导入、SMTP 代发、原生别名、双向标记同步已合入，需要显式开关（如 `ATOMAN_MAIL_EXTERNAL_IMAP_ENABLED`），尚未做生产验收。
- **图书**：书架绑定具体文件，版本支持草稿与推荐文件；上传前查重并比较文件质量证据；PDF 页内精确续读，跳转位置与最远进度分开记录；4 种纸张主题；章节笔记锚点、跨版本笔记汇总、笔记分享与撤销、划线回收站；trigram 全文检索与分面聚合。
- **搜索 / Feed**：统一搜索页与即时预览，`scope=mine` 搜索个人书籍和歌单；探索页推荐理由、批量订阅与滚动恢复；订阅流全文检索（相关度排序、正文片段，见下方发布门禁）。
- **音乐 / 播放器**：播放状态持久化与恢复、多标签页协调、MediaSession 系统控件；歌词画中画、独立翻译 LRC 导入、自动滚动；专辑与歌曲归档及带审计的恢复；导入、合并和播放进度加租约与行锁；播放会话记录设备与电台信息，用于跨设备接力。
- **移动端 Web App**：博客、书籍、订阅、消息、我的五个空间接入真实接口；全屏播放器重做，迷你播放器与底部导航合并；歌单编辑、排序与删除；虚拟键盘弹出时 sheet 与回复栏避让。
- **编辑器 / 博客**：选区浮动工具栏、斜杠命令、代码 / 公式 / 表格实时预览、脚注、409 冲突处理、封面拖放上传、标题大纲。
- **SEO 与安全**：边缘 SSR 输出公开书籍与音乐页面及 sitemap，无效与私有路由返回 noindex 404；外链确认，拦截任意 iframe 与自动播放的远程音频；升级 DOMPurify、x/net 等依赖；开发库与存储端口只绑定回环地址。
- **运维**：备份加密并在隔离环境验证恢复；不可变、非 root 的运行时激活，API 与 worker 成对回滚；NAS 通过只读 SFTP 断点续传拉取；邮件胶囊隔离恢复；watchdog 每 10 分钟检查。
- **平台与工程**：worker 租约 fencing，IndexNow 与 S3 清理改为持久任务，补齐 122 个路由的 Swagger 注解；前端类型、API 客户端与 store 按领域拆分。

### 发布前必须完成

前端 `main` 已在线上，但下列功能依赖的后端提交在 `e5dda0e9` 之后，尚未部署：

| 前端功能 | 当前线上表现 | 需要的后端提交 |
| --- | --- | --- |
| 书籍举报（`a109e4f3`） | 阅读器"举报"提交报错，管理台显示"加载举报失败" | `96ab298b` |
| 订阅流全文检索（`c3383333`） | 退回旧的 timeline 搜索，"相关 / 最新"切换无效 | `9ff20757`、`30d4365e` |
| 客户端错误上报（`76842e12`） | 收到 404 后静默停止，后端收不到数据 | `1d6ce3c8` |

后端部署包含两处 schema 变更：

- `internal/migrations/feed_fulltext_search.go`：创建 `pg_trgm`（无权限时跳过）和 `atoman_feed_search_document_v1` 函数，并在 `feed_items` 上用 `CREATE INDEX CONCURRENTLY` 建 trigram 索引。`feed_items` 在 2026-11-03 约 36 万行，建索引耗时尚未评估。
- `client_error_daily` 表：由 migrationrunner 的 AutoMigrate 创建（`internal/model/client_error.go`）。

部署前确认备份新鲜度，在生产数据副本上评估建索引耗时，再按 `docs/operations/production-runtime.md` 激活。后端部署后，ROADMAP P0 中"订阅流全文检索已可用"才与线上一致。

### 已知限制

- 发往 Gmail 的邮件曾进入垃圾箱，之后没有新的收件箱落点证据。
- 全站加密包未完整到达 NAS，全站独立恢复未验证（`offsite_transfer_confirmed=false`）。
- 没有独立于 VPS 的外部 uptime 监控；VPS 整体宕机时本机告警无法发出。
- MTA-STS 仍为 testing 模式。
- Feed 去重快照分页未上线，读路径保持原逻辑。
- 图书：staging 下的 HTTPS / cookie / CSRF 未测；划线与合集导出格式未对齐，笔记 PDF 导出未实现。

### 验证

- Backend CI（ubuntu-24.04 + PostgreSQL 16）：隔离脚本测试、`scripts/ops` Python 单测、部署与回滚脚本测试、`make swagger-check`、`go test ./...`、`govulncheck`、`go vet`、`go build`；`bea8654f` 全部通过。
- Frontend CI：type-check（vue-tsc 堆上限 4 GB）、生产与移动端构建、两片单测、e2e smoke、依赖审计；单测 spec 从 519 个增加到 638 个。
- CI 不覆盖：真实 Stalwart / IMAP、Feed 规模测试（需环境变量）、生产登录态的浏览器验收。

## English

### Overview

Atoman 0.4.0 adds a self-hosted mail service with Webmail, file-based book management with exact resume, unified search, a rebuilt mobile web app, and operations work including verified encrypted backup restores and an immutable runtime. Backend builds require Go 1.26.11 (GO-2026-6859).

### Highlights

- **Mail**: self-hosted mailboxes and Webmail on Stalwart + JMAP: admin invitations, user-chosen addresses, a durable outbox with five-second undo, private R2 storage; contacts, signatures, advanced search, scheduled send, site-wide unread counts; private labels and Snooze / follow-up reminders (Webmail-only). External IMAP, history import, SMTP send-as, native aliases and two-way flag sync are merged behind explicit flags (e.g. `ATOMAN_MAIL_EXTERNAL_IMAP_ENABLED`) and have no production acceptance yet.
- **Books**: shelves bind to concrete files; editions support drafts and a recommended file; duplicate checks and file-quality evidence before upload; exact in-page PDF resume with jump position kept separate from furthest progress; four paper themes; chapter note anchors, cross-edition note aggregation, revocable note sharing, highlight trash; trigram full-text search and facets.
- **Search / Feed**: unified search page with instant preview, `scope=mine` for personal books and playlists; explore reasons, batch subscribe and scroll restore; ranked subscribed full-text search (see release gate below).
- **Music / player**: persisted and restorable playback, multi-tab coordination, MediaSession; lyrics picture-in-picture, standalone translation LRC import, auto-scroll; album and song archiving with audited restore; leases and row locks for import, merge and progress; device and radio metadata for cross-device handoff.
- **Mobile web app**: blog, books, feed, messages and profile spaces on real APIs; rebuilt full-screen player with the mini player merged into the bottom bar; playlist edit, reorder and delete; keyboard-aware sheets.
- **Editor / blog**: selection toolbar, slash commands, live previews for code, math and tables, footnotes, 409 conflict handling, cover drag-and-drop, heading outline.
- **SEO and security**: edge SSR for public book and music pages and sitemaps, noindex 404 for invalid and private routes; external link confirmation, blocking arbitrary iframes and autoplaying remote audio; dependency upgrades (DOMPurify, x/net); dev database and storage ports bound to loopback.
- **Operations**: encrypted backups with isolated restore verification; immutable non-root runtime activation with paired API / worker rollback; resumable read-only SFTP pulls to NAS; isolated mail capsule restore; a watchdog every 10 minutes.
- **Platform**: worker lease fencing, durable IndexNow and S3 cleanup jobs, Swagger annotations for 122 previously undocumented routes; frontend types, API clients and stores split by domain.

### Must complete before release

The frontend `main` is live, but these features depend on backend commits after `e5dda0e9` that are not deployed: book reports (`a109e4f3` needs `96ab298b`; submitting currently fails), ranked feed full-text search (`c3383333` needs `9ff20757`, `30d4365e`; falls back to timeline search and the relevance toggle has no effect), and client error reporting (`76842e12` needs `1d6ce3c8`; stops silently on 404).

The backend deploy changes the schema: `feed_fulltext_search.go` creates `pg_trgm` (skipped without privilege), the `atoman_feed_search_document_v1` function and a trigram index on `feed_items` via `CREATE INDEX CONCURRENTLY` (about 360k rows on 2026-11-03; build time not measured), and AutoMigrate creates `client_error_daily`. Check backup freshness and measure the index build on a production data copy before activating per `docs/operations/production-runtime.md`.

### Known limitations

- Mail sent to Gmail previously landed in spam; there is no newer inbox-placement evidence.
- The full-site encrypted bundle has not fully reached the NAS and a full-site restore is unverified (`offsite_transfer_confirmed=false`).
- No external uptime monitoring independent of the VPS.
- MTA-STS remains in testing mode.
- Feed dedup snapshot pagination is not enabled.
- Books: HTTPS / cookie / CSRF untested on staging; highlight and collection export formats not aligned; note PDF export not implemented.

### Verification

- Backend CI (ubuntu-24.04 + PostgreSQL 16): isolated script tests, `scripts/ops` Python tests, deploy and rollback script tests, `make swagger-check`, `go test ./...`, `govulncheck`, `go vet`, `go build`; all pass on `bea8654f`.
- Frontend CI: type-check (vue-tsc with a 4 GB heap), production and mobile builds, two unit-test shards, e2e smoke, dependency audit; unit specs grew from 519 to 638.
- Not covered by CI: real Stalwart / IMAP, feed scale tests (env-gated), authenticated production browser acceptance.
