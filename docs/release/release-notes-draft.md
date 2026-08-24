# Release Notes Draft

## 中文

### 版本概览

Atoman 0.2.0 让订阅、博客和音乐形成可用的核心产品闭环，并补齐 Feed/RSS 内容发现、同步和持久化基础能力。

### 主要进展

- **订阅与 Feed**：支持内部频道和外部 RSS/Atom 源订阅、批量订阅、OPML 源目录导入、Explore 来源发现、语言筛选，以及暂停/恢复订阅。
- **订阅可靠性**：重复订阅保持幂等，RSS 抓取支持 GUID/link 去重、软删除恢复、条件请求、失败退避、主机并发限制和跨实例租约。
- **博客**：统一 canonical 内容模型，完善博客推荐、语言筛选、收藏夹、合集、文章版本、互动统计和公开内容接口。
- **音乐**：完善音乐发现、艺人和专辑浏览、播放队列、播放历史、歌单管理、Wiki 修订和推荐基础能力。
- **生产稳定性**：补充 PostgreSQL 迁移兼容、Feed 抓取状态迁移、统一内容回填保护和启动/运行时健康验证。

### 验证

- PostgreSQL 集成测试覆盖订阅幂等、FeedItem 去重与恢复、RSS 租约和迁移流程。
- Backend `go build ./...` 通过。
- Frontend TypeScript 严格类型检查通过。

## English

### Overview

Atoman 0.2.0 completes the core product loop for subscriptions, Blog, and Music, while strengthening Feed/RSS discovery, synchronization, and persistence.

### Highlights

- **Subscriptions and Feed**: Added subscriptions for internal channels and external RSS/Atom sources, batch subscription, global OPML source catalogs, Explore source discovery, language filtering, and pause/resume behavior.
- **Subscription reliability**: Duplicate subscriptions are idempotent; RSS ingestion now supports GUID/link deduplication, soft-delete recovery, conditional requests, failure backoff, per-host concurrency limits, and cross-instance leases.
- **Blog**: Consolidated the canonical content model and improved Blog recommendations, language filtering, bookmark folders, collections, post versions, engagement metrics, and public content APIs.
- **Music**: Improved music discovery, artist and album browsing, playback queues, listening history, playlist management, Wiki revisions, and recommendation foundations.
- **Production stability**: Added safer PostgreSQL migrations, Feed fetch-state migrations, unified-content backfill protection, and startup/runtime health validation.

### Verification

- PostgreSQL integration tests cover subscription idempotency, FeedItem deduplication and recovery, RSS leases, and migration flows.
- Backend `go build ./...` passes.
- Frontend strict TypeScript checks pass.
