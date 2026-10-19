# Release Notes Draft

## 中文

### 版本概览

Atoman 0.3.0 上线完整的 Books v2 公共书目与阅读体系、用户自定义主题、统一评分与播客单集评分，并完成一轮全仓工程治理：CI 历史上首次转绿、接口文档恢复自动生成并通过门禁校验、静态检查全类别清零。

### 主要进展

- **Books v2**：开放书目直接建目与检索，电子书资源不可变修订链、扫描件分片上传、修订级书签/划线与云阅读进度、私有书库与笔记迁移、公开笔记快照、阅读统计、声望加权评分与合格榜单、书单草稿与公共发现、修订锚定讨论与内容评审，以及 Wiki 社区（即时编辑、可预览合并、冲突与回滚、申诉/举报流程）。
- **评分**：统一评分冷启动，播客单集进入评分体系，新增真实 API 验收测试夹具。
- **主题**：用户自定义主题（模型、迁移、CSS 清洗器与 API），前端提供 ThemeCustomizerDrawer 与 CreatorThemeScope 封装。
- **短内容**：短笔记支持歌曲/专辑/视频媒体附件与元数据展示。
- **私信**：按阅读者身份同步已读状态，MessageDTO 暴露 ReadAt 并实时通知发送方。
- **音乐**：播放事件完整性修复、合并历史保护、导入可靠性改进，全局搜索支持按 UUID 定位音乐与视频条目。
- **工程治理**：CI 修复并连续绿灯（此前 434 次全红）、swagger 文档恢复生成并以门禁强制同步、启动强制显式 ENV、迁移系统账号停用登录、被忽略错误全部补传播、死代码清理与静态检查清零、部署文档补齐必需环境变量。

### 验证

- PostgreSQL 集成测试覆盖迁移、书架阅读流、评分冷启动与音乐导入。
- Backend `go build` / `go vet` / `go test ./...` 全部通过，CI 连续通过。
- Swagger 契约测试（12 个）与 `make swagger-check` 门禁通过。
- Frontend TypeScript 严格类型检查、单测与 e2e smoke 通过。

## English

### Overview

Atoman 0.3.0 ships the complete Books v2 public catalog and reading system, user themes, unified ratings with podcast episode support, and a full engineering pass: CI green for the first time in the project's history, regenerated API docs enforced by a drift gate, and zero static analysis findings.

### Highlights

- **Books v2**: direct public catalog creation and search; immutable ebook resource revisions with scanned multipart uploads; revision-pinned bookmarks, highlights and cloud reading progress; private libraries with recoverable note transfers; public note snapshots and reading statistics; reputation-weighted ratings and qualified rankings; book list drafts with public discovery; revision-anchored discussions and content reviews; and the Wiki community (immediate edits, previewed reversible merges, conflict handling and rollback, appeals and reports).
- **Ratings**: unified rating cold start, podcast episodes join the rating system, plus an isolated real-API acceptance fixture.
- **Themes**: user-defined themes (model, migration, CSS sanitizer, API) with the frontend ThemeCustomizerDrawer and CreatorThemeScope container.
- **Short notes**: media attachments for songs, albums and videos with enriched metadata.
- **Messages**: read status synchronized per reader identity; MessageDTO exposes ReadAt and notifies the sender in real time.
- **Music**: playback event integrity fixes, preserved merge history, import reliability, and global search by UUID for music and video entries.
- **Engineering**: CI repaired and consistently green (previously 434 consecutive failures), swagger docs regenerated and enforced by a drift gate, explicit ENV required at startup, the migration system account can no longer log in, previously ignored errors now propagate, dead code removed and static analysis at zero findings, deployment docs cover required environment variables.

### Verification

- PostgreSQL integration tests cover migrations, book reading flows, rating cold start and music imports.
- Backend `go build` / `go vet` / `go test ./...` pass and CI is consistently green.
- Swagger contract tests (12) and the `make swagger-check` gate pass.
- Frontend strict TypeScript checks, unit tests and e2e smoke pass.
