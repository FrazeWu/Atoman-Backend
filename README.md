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
# 内存受限环境：限制 Go 的包级并发。
make build-low-memory

# 只运行受影响包的定向测试；PACKAGE 必填。
make test-focused PACKAGE=./internal/modules/blog TEST_ARGS='-run TestPostRating -count=1'

# 迁移和 API 契约稳定后再执行。
make migrate
make swagger
```

常规构建和完整测试仅在发布前或跨模块变更后运行：

```bash
go build ./...
go test ./...
```

开发环境读取 `.env.dev`，生产环境读取 `.env.prod`。

## IP 归属地

登录记录的城市级归属地使用 MaxMind GeoLite2 City 数据库。生产环境将可读的 `.mmdb` 文件保存到稳定路径，并在 `.env.prod` 设置绝对路径：

```dotenv
GEOIP_DB_PATH=/var/lib/atoman/geoip/GeoLite2-City.mmdb
```

数据库文件更新后无需重启服务，后续请求会自动重新加载。Cloudflare 可用时会在城市库不可用的情况下保留国家代码；要显示城市和地区仍必须配置 GeoLite2 City 数据库。历史登录记录缺少位置时，管理端会按保存的 IP 重新解析。

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
