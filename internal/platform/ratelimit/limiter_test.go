package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestRedisLimiterUsesSharedCounter(t *testing.T) {
	var calls int
	limiter := newWithRedisAllow(func(_ context.Context, key string, limit int, duration time.Duration) (bool, time.Duration, error) {
		calls++
		if key != "login:alice" || limit != 2 || duration != time.Minute {
			t.Fatalf("unexpected redis limiter arguments: key=%q limit=%d duration=%s", key, limit, duration)
		}
		return false, 37 * time.Second, nil
	})

	allowed, retryAfter := limiter.Allow("login:alice", 2, time.Minute)
	if allowed || retryAfter != 37*time.Second {
		t.Fatalf("expected shared limiter result, got allowed=%v retry=%s", allowed, retryAfter)
	}
	if calls != 1 {
		t.Fatalf("expected one redis call, got %d", calls)
	}
}

func TestRedisLimiterFallsBackToLocalWindow(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	limiter := newWithRedisAllow(func(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
		return false, 0, context.DeadlineExceeded
	})
	limiter.now = func() time.Time { return now }

	if allowed, _ := limiter.Allow("login:alice", 1, time.Minute); !allowed {
		t.Fatal("first fallback request should be allowed")
	}
	if allowed, retryAfter := limiter.Allow("login:alice", 1, time.Minute); allowed || retryAfter != time.Minute {
		t.Fatalf("expected local fallback window, got allowed=%v retry=%s", allowed, retryAfter)
	}
}

func TestLimiterBlocksUntilWindowExpires(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	limiter := New()
	limiter.now = func() time.Time { return now }
	for attempt := 1; attempt <= 2; attempt++ {
		allowed, _ := limiter.Allow("login:alice", 2, time.Minute)
		if !allowed {
			t.Fatalf("attempt %d should be allowed", attempt)
		}
	}
	allowed, retryAfter := limiter.Allow("login:alice", 2, time.Minute)
	if allowed || retryAfter != time.Minute {
		t.Fatalf("expected blocked request with one minute retry, got allowed=%v retry=%s", allowed, retryAfter)
	}
	now = now.Add(time.Minute)
	allowed, _ = limiter.Allow("login:alice", 2, time.Minute)
	if !allowed {
		t.Fatal("request should be allowed after the window expires")
	}
}

func TestLimiterRemovesExpiredWindows(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	limiter := New()
	limiter.now = func() time.Time { return now }
	limiter.Allow("expired", 1, time.Minute)

	now = now.Add(2 * time.Minute)
	limiter.Allow("active", 1, time.Minute)

	if _, exists := limiter.windows["expired"]; exists {
		t.Fatal("expired window should be removed during cleanup")
	}
	if _, exists := limiter.windows["active"]; !exists {
		t.Fatal("active window should remain after cleanup")
	}
}
