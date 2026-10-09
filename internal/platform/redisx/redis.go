package redisx

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const defaultNamespace = "atoman"

// Namespace isolates Redis data between development, staging and production.
// The default keeps existing installations backward compatible.
func Namespace() string {
	raw := strings.TrimSpace(os.Getenv("REDIS_NAMESPACE"))
	if raw == "" {
		return defaultNamespace
	}
	return strings.Trim(raw, ":")
}

// Key adds the configured namespace exactly once to every shared Redis key.
func Key(raw string) string {
	raw = strings.Trim(strings.TrimSpace(raw), ":")
	if raw == "" {
		return Namespace()
	}
	namespace := Namespace()
	if raw == namespace || strings.HasPrefix(raw, namespace+":") {
		return raw
	}
	return namespace + ":" + raw
}

var (
	clientOnce sync.Once
	client     *redis.Client
)

func newClientFromURL(rawURL string) (*redis.Client, error) {
	options, err := redis.ParseURL(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, err
	}
	options.DialTimeout = 100 * time.Millisecond
	options.ReadTimeout = 100 * time.Millisecond
	options.WriteTimeout = 100 * time.Millisecond
	return redis.NewClient(options), nil
}

func ClientFromEnv() *redis.Client {
	clientOnce.Do(func() {
		if strings.TrimSpace(os.Getenv("REDIS_URL")) == "" {
			return
		}
		client, _ = newClientFromURL(os.Getenv("REDIS_URL"))
	})
	return client
}

type HealthStatus struct {
	Configured bool   `json:"configured"`
	Reachable  bool   `json:"reachable"`
	Error      string `json:"error,omitempty"`
}

func Check(ctx context.Context) HealthStatus {
	if strings.TrimSpace(os.Getenv("REDIS_URL")) == "" {
		return HealthStatus{}
	}
	redisClient := ClientFromEnv()
	if redisClient == nil {
		return HealthStatus{Configured: true, Error: "invalid REDIS_URL"}
	}
	if err := redisClient.Ping(ctx).Err(); err != nil {
		return HealthStatus{Configured: true, Error: "unreachable"}
	}
	return HealthStatus{Configured: true, Reachable: true}
}
