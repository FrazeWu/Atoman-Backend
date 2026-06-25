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
go run cmd/start_server/main.go
go run cmd/create_admin/main.go
```

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
cd /home/fa/Atoman-Backend
go build -o start_server ./cmd/start_server
```

The production runtime reads environment variables from:

```bash
/home/fa/Atoman-Backend/.env.prod
```

Current deployment uses:

- `BASE_URL=https://api.atoman.org`
- PostgreSQL via `DATABASE_URL`
- Cloudflare R2 via `S3_*` and `AWS_*`

## systemd Deployment

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
ExecStart=/home/fa/Atoman-Backend/start_server
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
- `STORAGE_TYPE=s3` is expected in production. Startup now fails fast if S3 initialization or validation fails.
