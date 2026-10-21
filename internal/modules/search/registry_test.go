package search

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	module string
	types  []TypeCapability
	mine   bool
	calls  atomic.Int32
	search func(context.Context, Viewer, ProviderSearchRequest) (ProviderSearchResult, error)
}

func (p *mockProvider) Module() string { return p.module }
func (p *mockProvider) Capabilities(_ context.Context, _ Viewer, scope Scope) ModuleCapabilities {
	if scope == ScopeMine && !p.mine {
		return ModuleCapabilities{}
	}
	return ModuleCapabilities{Label: p.module, Types: p.types}
}
func (p *mockProvider) Search(ctx context.Context, viewer Viewer, req ProviderSearchRequest) (ProviderSearchResult, error) {
	p.calls.Add(1)
	if p.search != nil {
		return p.search(ctx, viewer, req)
	}
	return ProviderSearchResult{Items: []SearchHit{}}, nil
}
func testType(key string) TypeCapability {
	return TypeCapability{Key: key, Label: key, Sorts: []SortDefinition{{Key: "relevance", Label: "相关度"}, {Key: "recent", Label: "最新"}}, DefaultSort: "relevance", DiscoverySort: "recent", AllowEmptyQuery: true}
}
func testRegistry(t *testing.T, providers ...SearchProvider) *Registry {
	t.Helper()
	r := NewRegistry()
	for _, provider := range providers {
		require.NoError(t, r.Register(provider))
	}
	return r
}
func testRequest() SearchRequest {
	return SearchRequest{Query: "三体", Scope: ScopePublic, View: ViewGrouped, Page: 1, PageSize: 24}
}

func TestRegistryRejectsDuplicateModule(t *testing.T) {
	r := NewRegistry()
	require.Error(t, r.Register(nil))
	require.Error(t, r.Register(&mockProvider{module: "invalid.module"}))
	require.NoError(t, r.Register(&mockProvider{module: "books", types: []TypeCapability{testType("work")}}))
	require.Error(t, r.Register(&mockProvider{module: "books"}))
	require.Equal(t, []string{"books"}, r.Modules())
	_, ok := r.Get("books")
	require.True(t, ok)
}

func TestRegistryRejectsUnsupportedType(t *testing.T) {
	r := testRegistry(t, &mockProvider{module: "books", types: []TypeCapability{testType("work")}})
	req := testRequest()
	for _, module := range []string{"unknown", "books"} {
		req.Module, req.Types = module, []string{"unknown"}
		_, err := r.Resolve(context.Background(), req, Viewer{})
		require.Error(t, err)
	}
	req.Scope, req.Types = ScopeMine, []string{"work"}
	_, err := r.Resolve(context.Background(), req, Viewer{ID: uuid.New()})
	require.Error(t, err)
}

func TestRegistryEnforcesModuleAccess(t *testing.T) {
	loads := 0
	r := NewRegistry(func(ctx context.Context) (map[string]bool, error) {
		loads++
		return map[string]bool{"books": false, "music": true}, ctx.Err()
	})
	book := &mockProvider{module: "books", types: []TypeCapability{testType("work")}}
	music := &mockProvider{module: "music", types: []TypeCapability{testType("song")}}
	require.NoError(t, r.Register(book))
	require.NoError(t, r.Register(music))
	tasks, err := r.Resolve(context.Background(), testRequest(), Viewer{})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "music", tasks[0].Module)
	require.Equal(t, 1, loads)
	req := testRequest()
	req.Module = "books"
	_, err = r.Resolve(context.Background(), req, Viewer{})
	require.ErrorContains(t, err, "site.module_disabled")
	require.Zero(t, book.calls.Load())
	r.access = func(context.Context) (map[string]bool, error) { return nil, errors.New("database unavailable") }
	_, err = r.Resolve(context.Background(), testRequest(), Viewer{})
	require.Error(t, err)
}

func TestSearchCapabilitiesMatchImplementedFilters(t *testing.T) {
	work, author := testType("work"), testType("author")
	filter := FilterDefinition{Key: "language", Label: "语言", Kind: "text", Multiple: true}
	work.Filters = []FilterDefinition{filter}
	r := testRegistry(t, &mockProvider{module: "books", types: []TypeCapability{work, author}})
	s := NewService(r)
	caps, err := s.Capabilities(context.Background(), Viewer{}, SearchRequest{Module: "books"})
	require.NoError(t, err)
	require.Empty(t, caps.Modules[0].Filters)
	caps, err = s.Capabilities(context.Background(), Viewer{}, SearchRequest{Module: "books", Types: []string{"work"}})
	require.NoError(t, err)
	require.Equal(t, []FilterDefinition{filter}, caps.Modules[0].Filters)
	require.Len(t, caps.Modules[0].Types, 1)
}

func TestCapabilitiesDescribeQueryRequiredTypesWithoutExecutingSearch(t *testing.T) {
	kind := testType("work")
	kind.AllowEmptyQuery = false
	p := &mockProvider{module: "books", types: []TypeCapability{kind}}
	s := NewService(testRegistry(t, p))
	req := SearchRequest{Module: "books", Types: []string{"work"}}
	caps, err := s.Capabilities(context.Background(), Viewer{}, req)
	require.NoError(t, err)
	require.False(t, caps.Modules[0].AllowEmptyQuery)
	_, err = s.Search(context.Background(), Viewer{}, req)
	require.Error(t, err)
	require.Zero(t, p.calls.Load())
}
