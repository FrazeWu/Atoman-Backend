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
```

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
