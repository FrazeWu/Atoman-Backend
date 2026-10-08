package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/redisx"

	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
)

const recommendationCacheTTL = 2 * time.Minute
const curatedSourceCacheTTL = 10 * time.Minute

type recommendationCacheEntry struct {
	Items []RecommendationItemDTO `json:"items"`
	Total int64                   `json:"total"`
}

type curatedSourceCacheEntry struct {
	Sources []ExploreSourceRow `json:"sources"`
}

type recommendationCache interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string, time.Duration) error
}

type redisRecommendationCache struct {
	client *redis.Client
}

func newRecommendationCacheFromEnv() recommendationCache {
	client := redisx.ClientFromEnv()
	if client == nil {
		return nil
	}
	return &redisRecommendationCache{client: client}
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
		"atoman:feed:recommendation:v2:%s:%s:%s:%s:%s:%s:%d:%d",
		encode(kind), encode(string(mode)), encode(category), encode(theme), encode(language), encode(search), page, pageSize,
	)
}

func curatedSourceCacheKey(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	return "atoman:feed:recommendation:sources:v2:" + url.QueryEscape(language)
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

func marshalCuratedSourceCacheEntry(entry curatedSourceCacheEntry) (string, error) {
	payload, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func unmarshalCuratedSourceCacheEntry(payload string) (curatedSourceCacheEntry, error) {
	var entry curatedSourceCacheEntry
	if err := json.Unmarshal([]byte(payload), &entry); err != nil {
		return curatedSourceCacheEntry{}, err
	}
	return entry, nil
}

type recommendationCachedValue[T any] struct {
	Value        T         `json:"value"`
	RefreshAfter time.Time `json:"refresh_after"`
}

// 新鲜度与保留时间分开：过期结果立即可读，刷新成功后才替换旧值。
func readCachedRecommendation[T any](s *Service, key string) (recommendationCachedValue[T], bool) {
	if s.recommendationCache == nil {
		return recommendationCachedValue[T]{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	payload, err := s.recommendationCache.Get(ctx, key)
	if err != nil {
		return recommendationCachedValue[T]{}, false
	}
	var entry recommendationCachedValue[T]
	if err := json.Unmarshal([]byte(payload), &entry); err != nil {
		return recommendationCachedValue[T]{}, false
	}
	return entry, true
}

func writeCachedRecommendation[T any](s *Service, key string, value T, freshTTL time.Duration) {
	if s.recommendationCache == nil {
		return
	}
	payload, err := json.Marshal(recommendationCachedValue[T]{Value: value, RefreshAfter: time.Now().Add(freshTTL)})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_ = s.recommendationCache.Set(ctx, key, string(payload), time.Hour)
}

func loadCachedRecommendation[T any](s *Service, key string, freshTTL time.Duration, load func() (T, error)) (T, error) {
	refresh := func() (any, error) {
		// 冷请求等待期间，前一个请求可能已经填好缓存。
		if cached, ok := readCachedRecommendation[T](s, key); ok && time.Now().Before(cached.RefreshAfter) {
			return cached.Value, nil
		}
		value, err := load()
		if err == nil {
			writeCachedRecommendation(s, key, value, freshTTL)
		}
		return value, err
	}
	if cached, ok := readCachedRecommendation[T](s, key); ok {
		if !time.Now().Before(cached.RefreshAfter) {
			// DoChan 同步登记 key，刷新在后台执行；同 key 只会有一次刷新。
			s.recommendationLoads.DoChan(key, refresh)
		}
		return cached.Value, nil
	}
	value, err, _ := s.recommendationLoads.Do(key, refresh)
	if err != nil {
		var zero T
		return zero, err
	}
	return value.(T), nil
}

func (s *Service) listCuratedRecommendationSources(language string) ([]ExploreSourceRow, error) {
	return loadCachedRecommendation(s, curatedSourceCacheKey(language), curatedSourceCacheTTL, func() ([]ExploreSourceRow, error) {
		return s.repo.ListCuratedExploreSources(recommendationFeaturedSourceLimit, language)
	})
}

func curatedSourceIDList(sources []ExploreSourceRow) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	return ids
}
