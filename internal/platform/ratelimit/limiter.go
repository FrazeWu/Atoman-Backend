package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"atoman/internal/platform/redisx"

	redis "github.com/redis/go-redis/v9"
)

const redisWindowScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
return {count, ttl}
`

type redisAllowFunc func(context.Context, string, int, time.Duration) (bool, time.Duration, error)

type window struct {
	count   int
	resetAt time.Time
}

type Limiter struct {
	mu         sync.Mutex
	windows    map[string]window
	now        func() time.Time
	cleanupAt  time.Time
	redisAllow redisAllowFunc
}

func New() *Limiter {
	return &Limiter{windows: make(map[string]window), now: time.Now}
}

// NewFromEnv enables a shared Redis window when REDIS_URL is configured.
// Redis failures fall back to the existing process-local limiter.
func NewFromEnv() *Limiter {
	limiter := New()
	client := sharedRedisClientFromEnv()
	if client != nil {
		limiter.redisAllow = redisAllowWithClient(client)
	}
	return limiter
}

func newWithRedisAllow(allow redisAllowFunc) *Limiter {
	limiter := New()
	limiter.redisAllow = allow
	return limiter
}

func (limiter *Limiter) Allow(key string, limit int, duration time.Duration) (bool, time.Duration) {
	if limiter.redisAllow != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		allowed, retryAfter, err := limiter.redisAllow(ctx, key, limit, duration)
		cancel()
		if err == nil {
			return allowed, retryAfter
		}
	}
	return limiter.allowLocal(key, limit, duration)
}

func (limiter *Limiter) allowLocal(key string, limit int, duration time.Duration) (bool, time.Duration) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	now := limiter.now()
	if limiter.cleanupAt.IsZero() || !now.Before(limiter.cleanupAt) {
		for windowKey, current := range limiter.windows {
			if !now.Before(current.resetAt) {
				delete(limiter.windows, windowKey)
			}
		}
		limiter.cleanupAt = now.Add(time.Minute)
	}
	current, exists := limiter.windows[key]
	if !exists || !now.Before(current.resetAt) {
		limiter.windows[key] = window{count: 1, resetAt: now.Add(duration)}
		return true, 0
	}
	if current.count >= limit {
		return false, current.resetAt.Sub(now)
	}
	current.count++
	limiter.windows[key] = current
	return true, 0
}

func sharedRedisClientFromEnv() *redis.Client {
	return redisx.ClientFromEnv()
}

func redisAllowWithClient(client *redis.Client) redisAllowFunc {
	return func(ctx context.Context, key string, limit int, duration time.Duration) (bool, time.Duration, error) {
		if duration <= 0 {
			duration = time.Second
		}
		result, err := client.Eval(ctx, redisWindowScript, []string{redisWindowKey(key)}, duration.Milliseconds()).Result()
		if err != nil {
			return false, 0, err
		}
		values, ok := result.([]interface{})
		if !ok || len(values) != 2 {
			return false, 0, redis.Nil
		}
		count, ok := redisInt64(values[0])
		if !ok {
			return false, 0, redis.Nil
		}
		if count <= int64(limit) {
			return true, 0, nil
		}
		ttl, ok := redisInt64(values[1])
		if !ok || ttl < 0 {
			return false, 0, redis.Nil
		}
		return false, time.Duration(ttl) * time.Millisecond, nil
	}
}

func redisInt64(value interface{}) (int64, bool) {
	parsed, ok := value.(int64)
	return parsed, ok
}

func redisWindowKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return redisx.Key("ratelimit:v1:" + hex.EncodeToString(digest[:]))
}
