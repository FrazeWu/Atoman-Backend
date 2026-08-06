package ratelimit

import (
	"sync"
	"time"
)

type window struct {
	count   int
	resetAt time.Time
}

type Limiter struct {
	mu        sync.Mutex
	windows   map[string]window
	now       func() time.Time
	cleanupAt time.Time
}

func New() *Limiter {
	return &Limiter{windows: make(map[string]window), now: time.Now}
}

func (limiter *Limiter) Allow(key string, limit int, duration time.Duration) (bool, time.Duration) {
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
