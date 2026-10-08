package cachex

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheConcurrentSameKeyLoadsOnce(t *testing.T) {
	cache := New[int](nil, "same-key", time.Minute, time.Hour)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	load := func(context.Context) (int, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return 42, nil
	}
	const readers = 16
	results := make(chan int, readers)
	errors := make(chan error, readers)
	var ready sync.WaitGroup
	ready.Add(readers)
	for range readers {
		go func() {
			ready.Done()
			value, err := cache.Get(context.Background(), "list", load)
			results <- value
			errors <- err
		}()
	}
	ready.Wait()
	waitCacheSignal(t, started, "首次加载未开始")
	close(release)
	for range readers {
		select {
		case value := <-results:
			if value != 42 {
				t.Fatalf("value = %d, want 42", value)
			}
		case <-time.After(time.Second):
			t.Fatal("同 key 请求未完成")
		}
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("load calls = %d, want 1", got)
	}
}

func TestCacheDifferentKeysDoNotBlockEachOther(t *testing.T) {
	cache := New[int](nil, "different-keys", time.Minute, time.Hour)
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = cache.Get(context.Background(), "slow", func(context.Context) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	waitCacheSignal(t, started, "慢 key 加载未开始")
	fastDone := make(chan struct{})
	go func() {
		defer close(fastDone)
		value, err := cache.Get(context.Background(), "fast", func(context.Context) (int, error) {
			return 2, nil
		})
		if err != nil || value != 2 {
			t.Errorf("fast result = %d, %v, want 2, nil", value, err)
		}
	}()
	waitCacheSignal(t, fastDone, "不同 key 被慢请求阻塞")
}

func TestCacheLoadFailureCanRetry(t *testing.T) {
	cache := New[int](nil, "retry", time.Minute, time.Hour)
	wantErr := errors.New("temporary failure")
	_, err := cache.Get(context.Background(), "list", func(context.Context) (int, error) {
		return 0, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	value, err := cache.Get(context.Background(), "list", func(context.Context) (int, error) {
		return 7, nil
	})
	if err != nil || value != 7 {
		t.Fatalf("retry result = %d, %v, want 7, nil", value, err)
	}
}

func TestCacheStaleValueReturnsDuringBackgroundRefresh(t *testing.T) {
	cache := New[int](nil, "stale", 10*time.Millisecond, time.Hour)
	_, err := cache.Get(context.Background(), "list", func(context.Context) (int, error) {
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	refreshStarted := make(chan struct{})
	refreshRelease := make(chan struct{})
	defer close(refreshRelease)
	returned := make(chan int, 1)
	go func() {
		value, err := cache.Get(context.Background(), "list", func(context.Context) (int, error) {
			close(refreshStarted)
			<-refreshRelease
			return 2, nil
		})
		if err != nil {
			t.Errorf("stale result error: %v", err)
		}
		returned <- value
	}()
	select {
	case value := <-returned:
		if value != 1 {
			t.Fatalf("stale value = %d, want 1", value)
		}
	case <-time.After(time.Second):
		t.Fatal("返回旧值等待了后台刷新")
	}
	waitCacheSignal(t, refreshStarted, "未触发后台刷新")
}

func TestCacheInvalidateDiscardsOldValues(t *testing.T) {
	cache := New[int](nil, "invalidate", time.Minute, time.Hour)
	_, err := cache.Get(context.Background(), "list", func(context.Context) (int, error) {
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cache.Invalidate()
	value, err := cache.Get(context.Background(), "list", func(context.Context) (int, error) {
		return 2, nil
	})
	if err != nil || value != 2 {
		t.Fatalf("after invalidation = %d, %v, want 2, nil", value, err)
	}
}

func TestCacheInvalidateDiscardsSharedValues(t *testing.T) {
	store := &memoryCacheStore{values: make(map[string]string)}
	first := New[int](store, "shared-invalidate", time.Minute, time.Hour)
	_, err := first.Get(context.Background(), "list", func(context.Context) (int, error) {
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second := New[int](store, "shared-invalidate", time.Minute, time.Hour)
	value, err := second.Get(context.Background(), "list", func(context.Context) (int, error) {
		t.Error("共享缓存未命中")
		return 0, nil
	})
	if err != nil || value != 1 {
		t.Fatalf("shared result = %d, %v, want 1, nil", value, err)
	}
	first.Invalidate()
	afterInvalidation := New[int](store, "shared-invalidate", time.Minute, time.Hour)
	value, err = afterInvalidation.Get(context.Background(), "list", func(context.Context) (int, error) {
		return 2, nil
	})
	if err != nil || value != 2 {
		t.Fatalf("shared result after invalidation = %d, %v, want 2, nil", value, err)
	}
}

func TestCacheInvalidationPreventsPendingLoadFromRestoringOldValue(t *testing.T) {
	cache := New[int](nil, "pending-invalidate", time.Minute, time.Hour)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = cache.Get(context.Background(), "list", func(context.Context) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	waitCacheSignal(t, started, "加载未开始")
	cache.Invalidate()
	close(release)
	waitCacheSignal(t, done, "旧请求未完成")
	value, err := cache.Get(context.Background(), "list", func(context.Context) (int, error) {
		return 2, nil
	})
	if err != nil || value != 2 {
		t.Fatalf("pending invalidation result = %d, %v, want 2, nil", value, err)
	}
}

type memoryCacheStore struct {
	mu     sync.Mutex
	values map[string]string
}

func (store *memoryCacheStore) Get(_ context.Context, key string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.values[key], nil
}

func (store *memoryCacheStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.values[key] = value
	return nil
}

func waitCacheSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}
