# Atoman Backend

Go 后端服务，负责 API、鉴权、数据模型、迁移、存储与 Nginx 配置。

## Tech Stack

- Go
- Gin
- GORM
- JWT
- PostgreSQL
- S3-compatible / MinIO
- Nginx
- Docker Compose

## Commands

```bash
go build ./...
go run cmd/migrate/main.go
go run cmd/start_server/main.go
go run cmd/create_admin/main.go
```

## Directory Architecture

| Path | 职责 |
|---|---|
| `cmd/` | 可执行入口 |
| `internal/handlers/` | HTTP 处理器 |
| `internal/model/` | 数据模型 |
| `internal/migrations/` | 数据库迁移 |
| `internal/middleware/` | 中间件 |
| `internal/service/` | 业务服务 |
| `internal/storage/` | 存储接口 |
| `nginx/` | Nginx 配置 |
| `docs/` | 后端相关文档 |

## Rules

- API 变化必须同步更新接口文档
- 修改完成前必须运行 `go build ./...`
- 不把前端构建产物或前端配置重新放回 backend
