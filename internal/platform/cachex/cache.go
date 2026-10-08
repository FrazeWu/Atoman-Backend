package cachex

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"atoman/internal/platform/redisx"
	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

type Store interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string, time.Duration) error
}

type redisStore struct{ client *redis.Client }

func (s redisStore) Get(ctx context.Context, key string) (string, error) {
	return s.client.Get(ctx, key).Result()
}
func (s redisStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}
func FromEnv() Store {
	if client := redisx.ClientFromEnv(); client != nil {
		return redisStore{client}
	}
	return nil
}

type entry[T any] struct {
	Value       T         `json:"value"`
	FreshUntil  time.Time `json:"fresh_until"`
	RetainUntil time.Time `json:"retain_until"`
}

type Cache[T any] struct {
	mu                  sync.Mutex
	values              map[string]entry[T]
	version             string
	store               Store
	prefix              string
	freshTTL, retainTTL time.Duration
	loads               singleflight.Group
}

func New[T any](store Store, prefix string, freshTTL, retainTTL time.Duration) *Cache[T] {
	return &Cache[T]{store: store, prefix: prefix, freshTTL: freshTTL, retainTTL: retainTTL, values: make(map[string]entry[T])}
}

func (c *Cache[T]) cacheKey(key string) string {
	c.mu.Lock()
	version := c.version
	c.mu.Unlock()
	if c.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		sharedVersion, err := c.store.Get(ctx, c.prefix+":version")
		cancel()
		if err == nil {
			version = sharedVersion
		}
	}
	return c.prefix + ":" + version + ":" + key
}

func (c *Cache[T]) read(key string) (entry[T], bool) {
	c.mu.Lock()
	cached, ok := c.values[key]
	c.mu.Unlock()
	if ok && time.Now().Before(cached.RetainUntil) {
		return cached, true
	}
	if c.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		payload, err := c.store.Get(ctx, key)
		cancel()
		if err == nil && json.Unmarshal([]byte(payload), &cached) == nil && time.Now().Before(cached.RetainUntil) {
			c.remember(key, cached)
			return cached, true
		}
	}
	return entry[T]{}, false
}

func (c *Cache[T]) remember(key string, value entry[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.values) >= 256 {
		for oldest := range c.values {
			delete(c.values, oldest)
			break
		}
	}
	c.values[key] = value
}

// 公共数据允许短期旧值；用户状态和权限判断不应放入此缓存。
func (c *Cache[T]) Get(ctx context.Context, key string, load func(context.Context) (T, error)) (T, error) {
	return c.get(ctx, key, load, true)
}

// 外部服务任务需要随调用方取消；公共页面刷新则独立于短暂的 HTTP 请求。
func (c *Cache[T]) GetCancelable(ctx context.Context, key string, load func(context.Context) (T, error)) (T, error) {
	return c.get(ctx, key, load, false)
}

func (c *Cache[T]) get(ctx context.Context, key string, load func(context.Context) (T, error), detach bool) (T, error) {
	key = c.cacheKey(key)
	refresh := func() (any, error) {
		if cached, ok := c.read(key); ok && time.Now().Before(cached.FreshUntil) {
			return cached.Value, nil
		}
		loadParent := ctx
		if detach {
			loadParent = context.WithoutCancel(ctx)
		}
		loadCtx, cancel := context.WithTimeout(loadParent, 30*time.Second)
		defer cancel()
		value, err := load(loadCtx)
		if err != nil {
			return value, err
		}
		cached := entry[T]{Value: value, FreshUntil: time.Now().Add(c.freshTTL), RetainUntil: time.Now().Add(c.retainTTL)}
		c.remember(key, cached)
		if c.store != nil {
			payload, marshalErr := json.Marshal(cached)
			if marshalErr == nil {
				writeCtx, writeCancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
				_ = c.store.Set(writeCtx, key, string(payload), c.retainTTL)
				writeCancel()
			}
		}
		return value, nil
	}
	if cached, ok := c.read(key); ok {
		if !time.Now().Before(cached.FreshUntil) {
			c.loads.DoChan(key, refresh)
		}
		return cached.Value, nil
	}
	select {
	case result := <-c.loads.DoChan(key, refresh):
		if result.Err != nil {
			var zero T
			return zero, result.Err
		}
		return result.Val.(T), nil
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// 版本键使其他进程和失效前尚未结束的刷新也无法重新命中旧数据。
func (c *Cache[T]) Invalidate() {
	version := uuid.NewString()
	c.mu.Lock()
	c.version = version
	c.values = make(map[string]entry[T])
	c.mu.Unlock()
	if c.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		defer cancel()
		_ = c.store.Set(ctx, c.prefix+":version", version, 0)
	}
}
