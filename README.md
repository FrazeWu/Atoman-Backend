# Atoman Backend

Go backend service for API, authentication, data models, migrations, storage, and host Nginx integration.

## Stack

- Go
- Gin
- GORM
- JWT
- PostgreSQL
- S3-compatible storage / Cloudflare R2
- Nginx
- systemd

## Common Commands

```bash
go build ./...
go run cmd/migrate/main.go
go run ./cmd/start_server --mode dev
go run cmd/create_admin/main.go
```

## Environment Files

Backend keeps three environment files:

- `.env.example`: local template without real secrets
- `.env.dev`: local development values
- `.env.prod`: production values

Use the startup command with an explicit mode:

```bash
go run ./cmd/start_server --mode dev
go run ./cmd/start_server --mode prod
```

Mode mapping:

- `dev` -> `.env.dev`
- `prod` -> `.env.prod`
- default mode is `dev`

Local development uses:

- `BASE_URL=http://localhost:8080`
- local PostgreSQL via `DATABASE_URL`
- local MinIO via `S3_ENDPOINT=http://localhost:9100`
- empty `TURNSTILE_SECRET_KEY`
- empty `AUTH_COOKIE_DOMAIN`

## Project Layout

| Path | Purpose |
| --- | --- |
| `cmd/` | Executable entrypoints |
| `internal/handlers/` | HTTP handlers |
| `internal/model/` | Data models |
| `internal/migrations/` | Database migrations |
| `internal/middleware/` | Middleware |
| `internal/service/` | Business services |
| `internal/storage/` | Storage integration |
| `nginx/` | Host Nginx config and certificate source files |
| `docs/` | Backend docs |

## Build And Run

Build the server binary:

```bash
go build -o start_server ./cmd/start_server
```

Production runtime can start with `--mode prod`, or read environment variables from `.env.prod` through `systemd`.

## Production Deployment Script

The production deployment script manages the backend and its host dependencies. It does not deploy the frontend, which remains on Cloudflare Pages.

Before the first deployment, install Git, Go 1.24+, Docker with the Compose plugin, systemd, Nginx, and curl. Also provide:

```bash
.env.prod
nginx/ssl/atoman.org.pem
nginx/ssl/atoman.org.key
```

Check the host without changing it:

```bash
scripts/deploy-production.sh check
```

Run the first deployment:

```bash
scripts/deploy-production.sh install
```

Deploy later updates:

```bash
scripts/deploy-production.sh update
```

The script:

- fast-forwards the local `main` branch to `origin/main`
- starts PostgreSQL with the `postgres` and `db-init` services in `docker-compose.dev.yml`
- builds the Go backend and starts it with systemd
- synchronizes the checked-in Nginx configuration and, during `install`, the TLS certificate
- uses Cloudflare R2 through `.env.prod`; it does not start MinIO in production
- runs the backend's automatic migrations on startup
- verifies both the local and public API endpoints
- restores the previous backend binary when startup or health checks fail

The deployment requires a clean `main` worktree. Run `install` once on a new host, then use `update` for subsequent deployments.

## Manual systemd Deployment

The deployment script is the recommended method. The following commands are retained as a manual reference and must be adjusted to match the actual repository path and service user.

Install the service:

```bash
sudo tee /etc/systemd/system/atoman-backend.service >/dev/null <<'EOF'
[Unit]
Description=Atoman Backend
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=fa
Group=fa
WorkingDirectory=/home/fa/Atoman-Backend
EnvironmentFile=/home/fa/Atoman-Backend/.env.prod
Environment=ENV=production
Environment=GIN_MODE=release
Environment=PORT=8080
ExecStart=/home/fa/Atoman-Backend/start_server --mode prod
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now atoman-backend
```

Check status:

```bash
sudo systemctl status atoman-backend
journalctl -u atoman-backend -f -q
```

## Nginx And SSL

This repo keeps the source certificate files in:

```bash
nginx/ssl/atoman.org.pem
nginx/ssl/atoman.org.key
```

Deploy them onto the host at:

```bash
/etc/nginx/ssl/api.atoman.org.pem
/etc/nginx/ssl/api.atoman.org.key
```

Install the certificate, Cloudflare real IP config, and site config:

```bash
sudo mkdir -p /etc/nginx/ssl
sudo cp /home/fa/Atoman-Backend/nginx/ssl/atoman.org.pem /etc/nginx/ssl/api.atoman.org.pem
sudo cp /home/fa/Atoman-Backend/nginx/ssl/atoman.org.key /etc/nginx/ssl/api.atoman.org.key
sudo chmod 600 /etc/nginx/ssl/api.atoman.org.key

sudo cp /home/fa/Atoman-Backend/nginx/conf.d/00-real-ip.conf /etc/nginx/conf.d/00-real-ip.conf
sudo cp /home/fa/Atoman-Backend/nginx/api.atoman.org.conf /etc/nginx/conf.d/api.atoman.org.conf

sudo nginx -t
sudo systemctl reload nginx
```

The checked-in host site config is:

```bash
nginx/api.atoman.org.conf
```

It serves:

- `https://api.atoman.org/api/...`
- `https://api.atoman.org/swagger/index.html`
- WebSocket routes

The root path `/` intentionally returns `404`.

## Verification

Local dependencies:

```bash
docker compose -f docker-compose.dev.yml up -d
```

Local backend:

```bash
curl -i http://127.0.0.1:8080/api/v1/site/access
curl -i http://127.0.0.1:8080/swagger/index.html
```

Public endpoint:

```bash
curl -i https://api.atoman.org/api/v1/site/access
curl -i https://api.atoman.org/swagger/index.html
```

TLS check:

```bash
openssl s_client -connect api.atoman.org:443 -servername api.atoman.org </dev/null
```

## Notes

- API changes must be reflected in the API docs.
- Run `go build ./...` before considering backend changes complete.
- Production does not use Docker Compose for the application process.
- `docker-compose.dev.yml` is only for local PostgreSQL and MinIO dependencies.
