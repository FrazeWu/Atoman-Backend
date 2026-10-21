package search

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/platform/authctx"
	"atoman/internal/platform/searchlimit"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func testHits(module, kind string, count int) []SearchHit {
	hits := make([]SearchHit, 0, count)
	for i := range count {
		hits = append(hits, SearchHit{Module: module, Type: kind, ID: fmt.Sprint(i), Title: "三体", Href: "/books/work/" + fmt.Sprint(i)})
	}
	return hits
}

func TestSearchPreviewSkipsExactCounts(t *testing.T) {
	p := &mockProvider{module: "books", types: []TypeCapability{testType("work")}}
	p.search = func(_ context.Context, _ Viewer, req ProviderSearchRequest) (ProviderSearchResult, error) {
		require.False(t, req.ExactTotal)
		require.Equal(t, PreviewLimit+1, req.Limit)
		require.Zero(t, req.Offset)
		return ProviderSearchResult{Items: testHits("books", req.Type, req.Limit)}, nil
	}
	result, err := NewService(testRegistry(t, p)).SearchPreview(context.Background(), Viewer{}, testRequest())
	require.NoError(t, err)
	require.Len(t, result.Groups[0].Items, PreviewLimit)
	require.Nil(t, result.Groups[0].Total)
	require.True(t, result.Groups[0].HasMore)
}

func TestSearchGroupedPreservesTypeOrder(t *testing.T) {
	ready := make(chan struct{})
	p := &mockProvider{module: "books", types: []TypeCapability{testType("work"), testType("author")}}
	p.search = func(_ context.Context, _ Viewer, req ProviderSearchRequest) (ProviderSearchResult, error) {
		if req.Type == "work" {
			<-ready
		} else {
			close(ready)
		}
		require.False(t, req.ExactTotal)
		require.Equal(t, GroupedLimit+1, req.Limit)
		return ProviderSearchResult{Items: testHits("books", req.Type, req.Limit)}, nil
	}
	result, err := NewService(testRegistry(t, p)).SearchGrouped(context.Background(), Viewer{}, testRequest())
	require.NoError(t, err)
	require.Equal(t, "work", result.Groups[0].Type)
	require.Equal(t, "author", result.Groups[1].Type)
	require.Len(t, result.Groups[0].Items, GroupedLimit)
	require.True(t, result.Groups[0].HasMore)
}

func TestSearchListUsesExactTotalAndStableOffset(t *testing.T) {
	p := &mockProvider{module: "books", types: []TypeCapability{testType("work")}}
	p.search = func(_ context.Context, _ Viewer, req ProviderSearchRequest) (ProviderSearchResult, error) {
		require.True(t, req.ExactTotal)
		require.Equal(t, 2, req.Limit)
		require.Equal(t, 2, req.Offset)
		total := int64(7)
		return ProviderSearchResult{Items: testHits("books", "work", 2), Total: &total}, nil
	}
	req := testRequest()
	req.Module, req.Types, req.Page, req.PageSize = "books", []string{"work"}, 2, 2
	result, err := NewService(testRegistry(t, p)).SearchList(context.Background(), Viewer{}, req)
	require.NoError(t, err)
	require.Equal(t, &Pagination{Page: 2, PageSize: 2, Total: 7, HasMore: true}, result.Pagination)
	require.Len(t, result.Items, 2)
	require.Equal(t, "books:work:0", result.Items[0].Key)
	// Provider 未提供准确计数时，不能伪装成 total=0。
	p.search = nil
	_, err = NewService(testRegistry(t, p)).SearchList(context.Background(), Viewer{}, req)
	require.ErrorContains(t, err, "search.unavailable")
}

func TestSearchReturnsPartialFailures(t *testing.T) {
	p := &mockProvider{module: "books", types: []TypeCapability{testType("work"), testType("author")}}
	p.search = func(_ context.Context, _ Viewer, req ProviderSearchRequest) (ProviderSearchResult, error) {
		if req.Type == "work" {
			return ProviderSearchResult{}, errors.New("secret database details")
		}
		return ProviderSearchResult{}, nil
	}
	s := NewService(testRegistry(t, p))
	result, err := s.Search(context.Background(), Viewer{}, testRequest())
	require.NoError(t, err)
	require.True(t, result.Partial)
	require.Equal(t, "search.provider_failed", result.Groups[0].Error.Code)
	require.NotContains(t, result.Groups[0].Error.Message, "secret")
	require.Empty(t, result.Groups[0].Items)
	require.Nil(t, result.Groups[1].Error)
	p.search = func(context.Context, Viewer, ProviderSearchRequest) (ProviderSearchResult, error) {
		return ProviderSearchResult{}, errors.New("failed")
	}
	_, err = s.Search(context.Background(), Viewer{}, testRequest())
	require.ErrorContains(t, err, "search.unavailable")
}

func TestSearchEmptyQueryDoesNotScanAllModules(t *testing.T) {
	p := &mockProvider{module: "books", types: []TypeCapability{testType("work")}}
	s := NewService(testRegistry(t, p))
	for _, req := range []SearchRequest{{}, {Query: "中", View: ViewPreview}, {Query: "", Module: "books"}} {
		result, err := s.Search(context.Background(), Viewer{}, req)
		require.NoError(t, err)
		require.Empty(t, result.Groups)
	}
	require.Zero(t, p.calls.Load())
	p.search = func(_ context.Context, _ Viewer, req ProviderSearchRequest) (ProviderSearchResult, error) {
		require.Equal(t, "recent", req.Sort)
		return ProviderSearchResult{}, nil
	}
	_, err := s.Search(context.Background(), Viewer{}, SearchRequest{Module: "books", Types: []string{"work"}})
	require.NoError(t, err)
	require.EqualValues(t, 1, p.calls.Load())
}

func TestSearchPrivateScopeValidatesBeforeProviders(t *testing.T) {
	p := &mockProvider{module: "books", mine: true, types: []TypeCapability{testType("work")}}
	s := NewService(testRegistry(t, p))
	req := testRequest()
	req.Scope = ScopeMine
	_, err := s.Search(context.Background(), Viewer{}, req)
	require.ErrorContains(t, err, "auth.unauthorized")
	_, err = s.Capabilities(context.Background(), Viewer{}, req)
	require.ErrorContains(t, err, "auth.unauthorized")
	require.Zero(t, p.calls.Load())
	viewer := Viewer{ID: uuid.New(), Username: "alice", Role: authctx.RoleModerator}
	p.search = func(_ context.Context, actual Viewer, req ProviderSearchRequest) (ProviderSearchResult, error) {
		require.Equal(t, viewer, actual)
		require.Equal(t, ScopeMine, req.Scope)
		return ProviderSearchResult{}, nil
	}
	_, err = s.Search(context.Background(), viewer, req)
	require.NoError(t, err)
}

func TestSearchRejectsUnsupportedFilterAndSortBeforeProviders(t *testing.T) {
	p := &mockProvider{module: "books", types: []TypeCapability{testType("work")}}
	s := NewService(testRegistry(t, p))
	req := testRequest()
	req.Filters = map[string][]string{"unknown": {"value"}}
	_, err := s.Search(context.Background(), Viewer{}, req)
	require.Error(t, err)
	req.Filters = nil
	req.Sort = "unknown"
	_, err = s.Search(context.Background(), Viewer{}, req)
	require.Error(t, err)
	req.Sort = ""
	req.View = ViewList
	_, err = s.Search(context.Background(), Viewer{}, req)
	require.Error(t, err)
	require.Zero(t, p.calls.Load())
}

func TestSearchNormalizesAndWhitelistsHits(t *testing.T) {
	kind := testType("work")
	kind.MetadataFields = []string{"year"}
	kind.SearchFields = []string{"title"}
	p := &mockProvider{module: "books", types: []TypeCapability{kind}}
	p.search = func(context.Context, Viewer, ProviderSearchRequest) (ProviderSearchResult, error) {
		hit := testHits("books", "work", 1)[0]
		hit.Metadata = map[string]any{"year": 2000, "secret": "private"}
		hit.MatchedFields = []string{"title", "private_note", "title"}
		return ProviderSearchResult{Items: []SearchHit{hit, hit}}, nil
	}
	s := NewService(testRegistry(t, p))
	result, err := s.Search(context.Background(), Viewer{}, testRequest())
	require.NoError(t, err)
	require.Len(t, result.Groups[0].Items, 1)
	hit := result.Groups[0].Items[0]
	require.Equal(t, map[string]any{"year": 2000}, hit.Metadata)
	require.Equal(t, []string{"title"}, hit.MatchedFields)
	p.search = func(context.Context, Viewer, ProviderSearchRequest) (ProviderSearchResult, error) {
		return ProviderSearchResult{Items: testHits("music", "song", 1)}, nil
	}
	_, err = s.Search(context.Background(), Viewer{}, testRequest())
	require.Error(t, err)
}

func TestSearchCancellationStopsProviderTasks(t *testing.T) {
	started := make(chan struct{}, 4)
	p := &mockProvider{module: "books", types: []TypeCapability{testType("a"), testType("b"), testType("c"), testType("d")}}
	p.search = func(ctx context.Context, _ Viewer, _ ProviderSearchRequest) (ProviderSearchResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return ProviderSearchResult{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := NewService(testRegistry(t, p)).Search(ctx, Viewer{}, testRequest()); done <- err }()
	<-started
	<-started
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("search did not stop")
	}
	require.EqualValues(t, 2, p.calls.Load())
}

func TestCanceledSearchDoesNotLoadCapabilitiesOrAccessSettings(t *testing.T) {
	r := NewRegistry(func(context.Context) (map[string]bool, error) {
		t.Fatal("canceled search queried site settings")
		return nil, nil
	})
	s := NewService(r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Search(ctx, Viewer{}, testRequest())
	require.ErrorIs(t, err, context.Canceled)
	_, err = s.Capabilities(ctx, Viewer{}, SearchRequest{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestSearchProviderTimeoutProducesPartialFailure(t *testing.T) {
	p := &mockProvider{module: "books", types: []TypeCapability{testType("slow"), testType("fast")}}
	p.search = func(ctx context.Context, _ Viewer, req ProviderSearchRequest) (ProviderSearchResult, error) {
		if req.Type == "slow" {
			<-ctx.Done()
			return ProviderSearchResult{}, ctx.Err()
		}
		return ProviderSearchResult{}, nil
	}
	s := NewService(testRegistry(t, p))
	s.providerTimeout = 20 * time.Millisecond
	result, err := s.Search(context.Background(), Viewer{}, testRequest())
	require.NoError(t, err)
	require.True(t, result.Partial)
	require.Equal(t, "search.provider_timeout", result.Groups[0].Error.Code)
}

func recordPeak(active, peak *atomic.Int32) {
	current := active.Add(1)
	for old := peak.Load(); current > old; old = peak.Load() {
		if peak.CompareAndSwap(old, current) {
			break
		}
	}
}

func TestSearchRespectsConcurrencyLimit(t *testing.T) {
	for _, processes := range []int{1, 12} {
		t.Run(fmt.Sprint(processes), func(t *testing.T) {
			var active, peak atomic.Int32
			started := make(chan struct{}, 24)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var wg sync.WaitGroup
			errors := make(chan error, processes)
			for range processes {
				p := &mockProvider{module: "books", types: []TypeCapability{testType("a"), testType("b"), testType("c"), testType("d")}}
				p.search = func(ctx context.Context, _ Viewer, _ ProviderSearchRequest) (ProviderSearchResult, error) {
					recordPeak(&active, &peak)
					defer active.Add(-1)
					select {
					case started <- struct{}{}:
					default:
					}
					select {
					case <-release:
						return ProviderSearchResult{}, nil
					case <-ctx.Done():
						return ProviderSearchResult{}, ctx.Err()
					}
				}
				s := NewService(testRegistry(t, p))
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := s.Search(context.Background(), Viewer{}, testRequest())
					errors <- err
				}()
			}
			limit := searchWorkerCount
			if processes > 1 {
				limit = searchlimit.Capacity
			}
			for range limit {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("workers did not start")
				}
			}
			require.EqualValues(t, limit, active.Load())
			unblock()
			wg.Wait()
			close(errors)
			for err := range errors {
				require.NoError(t, err)
			}
			require.LessOrEqual(t, int(peak.Load()), limit)
		})
	}
}
