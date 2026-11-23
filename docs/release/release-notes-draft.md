# Release Notes Draft — v0.4.1

> 草稿，尚未归档。上一版说明见 [v0.4.0.md](v0.4.0.md)。
> 范围：Backend `v0.4.0`（`92f6a30`）..`HEAD`。

## 中文

### 主要变更

- **性能优化**：移除请求热路径中的 `HasTable` 数据库元数据查询，引入连接池级别的表存在性缓存（`HasTableCached`）。
- **配置缓存**：将站点权限配置改为内存缓存，并在 `SiteSetting` 保存或删除时通过变更钩子联动失效。
- **接口文档**：补齐前端实际使用的 219 个路由的 Swagger 注解与文档产物。
- **数据库**：不涉及数据库 schema 迁移（仅新增内存缓存辅助逻辑与设置变更钩子）。

## English

### Highlights

- **Performance**: Removed per-request `HasTable` metadata queries on hot paths and added connection-pool-scoped table existence caching (`HasTableCached`).
- **Settings cache**: Cached site access settings in memory with automatic invalidation hooks on `SiteSetting` save/delete.
- **API docs**: Added Swagger annotations and regenerated docs for 219 frontend-used routes.
- **Database**: No database schema migrations (only in-memory cache helpers and setting-change hooks).
