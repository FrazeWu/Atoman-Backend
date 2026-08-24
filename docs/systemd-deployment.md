# Systemd Deployment

This deployment path runs the Go backend directly on the host and keeps Nginx on the host as the public gateway.

## Files

- `nginx/api.atoman.org.conf`: host Nginx reverse proxy config
- `nginx/conf.d/00-real-ip.conf`: Cloudflare real IP trust config
- `nginx/ssl/atoman.org.pem`: source certificate file kept in the repo
- `nginx/ssl/atoman.org.key`: source private key kept in the repo

## Assumptions

- The repo lives at `/home/fa/Atoman-Backend`
- The built binary lives at `/home/fa/Atoman-Backend/start_server`
- Runtime environment variables stay in `/home/fa/Atoman-Backend/.env.prod`
- The backend listens on `0.0.0.0:8080`
- Nginx reads the deployed certificate from `/etc/nginx/ssl/api.atoman.org.pem`
- Nginx reads the deployed private key from `/etc/nginx/ssl/api.atoman.org.key`

## Build

```bash
cd /home/fa/Atoman-Backend
go build -o start_server ./cmd/start_server
go build -o migrate ./cmd/migrate
go build -o music_import_worker ./cmd/music_import_worker
```

## Migrate

Production startup intentionally does not run schema migrations. Run the migration binary after building the release and before restarting either service:

```bash
cd /home/fa/Atoman-Backend
./migrate --env .env.prod
```

Do not restart the backend or worker when this command fails.

## GeoIP 数据库

登录记录的城市级归属地依赖 MaxMind GeoLite2 City。先创建 MaxMind 账户和许可证密钥，再将数据库下载到服务账号可读的固定路径：

```bash
sudo install -d -o fa -g fa /var/lib/atoman/geoip
curl --fail --location --user "$MAXMIND_ACCOUNT_ID:$MAXMIND_LICENSE_KEY" \
  'https://download.maxmind.com/geoip/databases/GeoLite2-City/download?suffix=tar.gz' \
  | sudo tar -xz --wildcards --strip-components=1 -C /var/lib/atoman/geoip '*/GeoLite2-City.mmdb'
sudo chown fa:fa /var/lib/atoman/geoip/GeoLite2-City.mmdb
```

在 `/home/fa/Atoman-Backend/.env.prod` 设置：

```dotenv
GEOIP_DB_PATH=/var/lib/atoman/geoip/GeoLite2-City.mmdb
```

定期以原子替换方式更新同一路径的数据库文件。服务会在下一次查询时自动加载新版文件；无需重启。Cloudflare 的 `CF-IPCountry` 仅在数据库不可用时提供国家级降级，不能替代城市库。

## Install systemd unit

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

Install the album import worker separately so queued imports and expired source-object cleanup continue when the HTTP service is idle:

```bash
sudo tee /etc/systemd/system/atoman-music-import-worker.service >/dev/null <<'EOF'
[Unit]
Description=Atoman Music Import Worker
After=network.target atoman-backend.service

[Service]
Type=simple
User=fa
Group=fa
WorkingDirectory=/home/fa/Atoman-Backend
EnvironmentFile=/home/fa/Atoman-Backend/.env.prod
# Requires ffmpeg, ffprobe and 7zz on PATH. Set MUSIC_PLAYBACK_BUCKET and MUSIC_PLAYBACK_URL_PREFIX.
ExecStart=/home/fa/Atoman-Backend/music_import_worker
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload
sudo systemctl enable --now atoman-music-import-worker
```

## Check service status

```bash
sudo systemctl status atoman-backend
journalctl -u atoman-backend -f
```

## Install host Nginx config

Install the certificate and Nginx config from the checked-in files:

```bash
sudo mkdir -p /etc/nginx/ssl
sudo cp /home/fa/Atoman-Backend/nginx/ssl/atoman.org.pem /etc/nginx/ssl/api.atoman.org.pem
sudo cp /home/fa/Atoman-Backend/nginx/ssl/atoman.org.key /etc/nginx/ssl/api.atoman.org.key
sudo chmod 600 /etc/nginx/ssl/api.atoman.org.key
sudo cp /home/fa/Atoman-Backend/nginx/conf.d/00-real-ip.conf /etc/nginx/conf.d/00-real-ip.conf
sudo cp /home/fa/Atoman-Backend/nginx/api.atoman.org.conf /etc/nginx/conf.d/api.atoman.org.conf
```

Then test and reload Nginx:

```bash
sudo nginx -t
sudo systemctl reload nginx
```

## Notes

- The backend will be reachable on host port `8080`. Restrict access with firewall rules if the port should not be public.
- If the public frontend origin changes, update `ALLOWED_ORIGINS` in `.env.prod`.
- The Nginx root path intentionally returns `404`; only API, Swagger, uploads, and WebSocket routes are proxied.
