package feed

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/modules/recommendation"
)

func TestHotRecommendationsReuseFreshCachedResults(t *testing.T) {
	for _, kind := range []string{"articles", "channels"} {
		t.Run(kind, func(t *testing.T) {
			db := newRecommendationTestDB(t)
			key := recommendationCacheKey(kind, recommendation.ModeHot, "blog", "all", "zh", "", 1, 20)
			payload, err := json.Marshal(recommendationCachedValue[recommendationCacheEntry]{
				Value: recommendationCacheEntry{
					Items: []RecommendationItemDTO{{ID: "cached-hot-item", Title: "Hot result"}},
					Total: 1,
				},
				RefreshAfter: time.Now().Add(time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}
			s := NewService(db)
			s.recommendationCache = &memoryRecommendationCache{values: map[string]string{key: string(payload)}}
			var items []RecommendationItemDTO
			var total int64
			if kind == "articles" {
				items, total, err = s.RecommendArticlesByMode(recommendation.ModeHot, "blog", "all", "zh", "", 1, 20)
			} else {
				items, total, err = s.RecommendChannelsByMode(recommendation.ModeHot, "blog", "all", "zh", 1, 20)
			}
			if err != nil || total != 1 || len(items) != 1 || items[0].ID != "cached-hot-item" {
				t.Fatalf("fresh hot cache was bypassed: items=%+v total=%d err=%v", items, total, err)
			}
		})
	}
}

type memoryRecommendationCache struct {
	mu     sync.Mutex
	values map[string]string
	writes chan struct{}
}

func (c *memoryRecommendationCache) Get(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if value, ok := c.values[key]; ok {
		return value, nil
	}
	return "", errors.New("cache miss")
}

func (c *memoryRecommendationCache) Set(_ context.Context, key, value string, _ time.Duration) error {
	c.mu.Lock()
	c.values[key] = value
	c.mu.Unlock()
	if c.writes != nil {
		c.writes <- struct{}{}
	}
	return nil
}

func TestRecommendationCacheCoalescesColdRequests(t *testing.T) {
	cache := &memoryRecommendationCache{values: map[string]string{}}
	s := &Service{recommendationCache: cache}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	load := func() (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "fresh", nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := loadCachedRecommendation(s, "articles", time.Minute, load)
			if err != nil || value != "fresh" {
				t.Errorf("got %q, %v", value, err)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("expected one cold load, got %d", calls.Load())
	}
}

func TestRecommendationCacheReturnsStaleWhileRefreshing(t *testing.T) {
	payload, _ := json.Marshal(recommendationCachedValue[string]{Value: "old", RefreshAfter: time.Now().Add(-time.Second)})
	cache := &memoryRecommendationCache{values: map[string]string{"articles": string(payload)}, writes: make(chan struct{}, 1)}
	s := &Service{recommendationCache: cache}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	load := func() (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "new", nil
	}
	defer close(release)
	for i := 0; i < 20; i++ {
		value, err := loadCachedRecommendation(s, "articles", time.Minute, load)
		if err != nil || value != "old" {
			t.Fatalf("stale request blocked or failed: %q, %v", value, err)
		}
	}
	<-started
	if calls.Load() != 1 {
		t.Fatalf("expected one refresh, got %d", calls.Load())
	}
	// 释放刷新后，读取者应看到新结果。
	release <- struct{}{}
	<-cache.writes
	value, err := loadCachedRecommendation(s, "articles", time.Minute, load)
	if err != nil || value != "new" {
		t.Fatalf("refresh not visible: %q, %v", value, err)
	}
}

func TestRecommendationCacheFailedRefreshKeepsPreviousValue(t *testing.T) {
	payload, _ := json.Marshal(recommendationCachedValue[string]{Value: "old", RefreshAfter: time.Now().Add(-time.Second)})
	cache := &memoryRecommendationCache{values: map[string]string{"articles": string(payload)}}
	s := &Service{recommendationCache: cache}
	value, err := loadCachedRecommendation(s, "articles", time.Minute, func() (string, error) { return "", errors.New("database unavailable") })
	if err != nil || value != "old" {
		t.Fatalf("expected usable previous value: %q, %v", value, err)
	}
	// 等待该 key 的刷新结束，再检查失败没有覆盖旧缓存。
	s.recommendationLoads.Do("articles", func() (any, error) { return nil, nil })
	stored, _ := cache.Get(context.Background(), "articles")
	if stored != string(payload) {
		t.Fatal("failed refresh overwrote cached value")
	}
}
