# Atoman Backend

## 简介

Atoman 的 Go 后端服务，负责 API、鉴权、内容数据、后台任务、对象存储和管理能力。

## 功能

- 用户与权限：JWT、OAuth、Turnstile、用户资料、访问控制和后台管理。
- 内容服务：Blog、短话、论坛、辩题、播客、视频和人物时间线。
- Feed / RSS：订阅分组、抓取、全文提取、搜索、阅读状态、收藏和规则。
- 音乐档案库：艺人、专辑、歌曲、歌单、导入、歌词注释和版本治理。
- Studio：频道、内容、数据、互动和发布管理接口。
- 通用能力：点赞评论、通知私信、用户屏蔽、内容保护和 Swagger 文档。

## 技术栈

- Go
- Gin
- GORM
- PostgreSQL
- JWT
- S3 兼容对象存储
- Nginx
- systemd

## 开发

本地 PostgreSQL 和 MinIO 由根目录的 `docker-compose.dev.yml` 提供。

```bash
docker compose -f ../docker-compose.dev.yml up -d
cp .env.example .env.dev
go run ./cmd/start_server --mode dev
```

常用命令：

```bash
go build ./...
go test ./...
go run cmd/migrate/main.go
go run cmd/create_admin/main.go
```

开发环境读取 `.env.dev`，生产环境读取 `.env.prod`。

## 部署

生产环境采用主机进程部署：

- Go 应用构建为主机二进制，由 systemd 管理。
- Nginx 负责反向代理、TLS 和站点入口。
- PostgreSQL 使用 Neon。
- 对象存储使用 Cloudflare R2。
- Cloudflare 位于公网入口前层并提供 CDN。

```bash
go build -o start_server ./cmd/start_server
./start_server --mode prod
```

开发计划见 [ROADMAP.md](./ROADMAP.md)。
