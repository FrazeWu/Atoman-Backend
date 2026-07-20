package ratelimit

import (
	"testing"
	"time"
)

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
