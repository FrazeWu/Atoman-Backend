package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"atoman/internal/modules/recommendation"

	redis "github.com/redis/go-redis/v9"
)

const recommendationCacheTTL = 2 * time.Minute

type recommendationCacheEntry struct {
	Items []RecommendationItemDTO `json:"items"`
	Total int64                   `json:"total"`
}

type recommendationCache interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string, time.Duration) error
}

type redisRecommendationCache struct {
	client *redis.Client
}

func newRecommendationCacheFromEnv() recommendationCache {
	rawURL := strings.TrimSpace(os.Getenv("REDIS_URL"))
	if rawURL == "" {
		return nil
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil
	}
	options.DialTimeout = 100 * time.Millisecond
	options.ReadTimeout = 100 * time.Millisecond
	options.WriteTimeout = 100 * time.Millisecond
	return &redisRecommendationCache{client: redis.NewClient(options)}
}

func (c *redisRecommendationCache) Get(ctx context.Context, key string) (string, error) {
	return c.client.Get(ctx, key).Result()
}

func (c *redisRecommendationCache) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}

func recommendationCacheKey(kind string, mode recommendation.Mode, category, theme, language, search string, page, pageSize int) string {
	encode := func(value string) string { return url.QueryEscape(strings.TrimSpace(value)) }
	return fmt.Sprintf(
		"atoman:feed:recommendation:v1:%s:%s:%s:%s:%s:%s:%d:%d",
		encode(kind), encode(string(mode)), encode(category), encode(theme), encode(language), encode(search), page, pageSize,
	)
}

func marshalRecommendationCacheEntry(entry recommendationCacheEntry) (string, error) {
	payload, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func unmarshalRecommendationCacheEntry(payload string) (recommendationCacheEntry, error) {
	var entry recommendationCacheEntry
	if err := json.Unmarshal([]byte(payload), &entry); err != nil {
		return recommendationCacheEntry{}, err
	}
	return entry, nil
}

func (s *Service) readRecommendationCache(key string) (recommendationCacheEntry, bool) {
	if s.recommendationCache == nil {
		return recommendationCacheEntry{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	payload, err := s.recommendationCache.Get(ctx, key)
	if err != nil {
		return recommendationCacheEntry{}, false
	}
	entry, err := unmarshalRecommendationCacheEntry(payload)
	if err != nil {
		return recommendationCacheEntry{}, false
	}
	return entry, true
}

func (s *Service) writeRecommendationCache(key string, entry recommendationCacheEntry) {
	if s.recommendationCache == nil {
		return
	}
	payload, err := marshalRecommendationCacheEntry(entry)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_ = s.recommendationCache.Set(ctx, key, payload, recommendationCacheTTL)
}
