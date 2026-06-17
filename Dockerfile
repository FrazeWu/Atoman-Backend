# Stage 1: build
FROM golang:1.24.3-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o main ./cmd/start_server

# Stage 2: runtime
FROM alpine:3.19
RUN apk --no-cache add ca-certificates tzdata
RUN addgroup -S appgroup && adduser -S appuser -G appgroup
WORKDIR /app
RUN mkdir -p /app/log && chown -R appuser:appgroup /app
COPY --from=builder /app/main .
USER appuser
EXPOSE 8080
CMD ["./main"]
