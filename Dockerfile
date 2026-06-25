# Stage 1: build
FROM golang:1.24.3-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o main ./cmd/start_server

# Stage 2: runtime
FROM alpine:3.19
RUN apk --no-cache add ca-certificates nginx su-exec tzdata
RUN addgroup -S appgroup && adduser -S appuser -G appgroup
WORKDIR /app
RUN mkdir -p /app/log /app/uploads /run/nginx \
    && chown -R appuser:appgroup /app /run/nginx /var/lib/nginx /var/log/nginx
COPY --from=builder /app/main .
COPY nginx/conf.d/00-real-ip.conf /etc/nginx/http.d/00-real-ip.conf
COPY deploy/nginx/backend-gateway.conf /etc/nginx/http.d/default.conf
COPY deploy/docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 80 443
CMD ["/entrypoint.sh"]
