package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/searchlimit"
)

const searchWorkerCount = 2

type Service struct {
	registry        *Registry
	providerTimeout time.Duration
}

func NewService(registry *Registry) *Service {
	return &Service{registry: registry, providerTimeout: 2 * time.Second}
}

func (s *Service) Search(ctx context.Context, viewer Viewer, req SearchRequest) (SearchResponse, error) {
	if err := ctx.Err(); err != nil {
		return SearchResponse{}, err
	}
	req, err := NormalizeSearchRequest(req)
	if err != nil {
		return SearchResponse{}, err
	}
	if err := ValidateScope(req.Scope, viewer); err != nil {
		return SearchResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tasks, err := s.registry.Resolve(ctx, req, viewer)
	if err != nil {
		return SearchResponse{}, err
	}
	response := SearchResponse{View: req.View, Scope: req.Scope, Query: req.Query, Groups: []SearchGroup{}, Items: []SearchHit{}}
	// 搜索首页和不足两字符的快捷搜索不调用任何内容 Provider。
	if (req.Query == "" && (req.Module == "" || len(req.Types) == 0)) || (req.View == ViewPreview && utf8.RuneCountInString(req.Query) < 2) {
		return response, nil
	}
	if len(tasks) == 0 {
		return response, unavailable()
	}
	groups, err := s.runProviderTasks(ctx, viewer, req, tasks)
	if err != nil {
		return response, err
	}
	failed := 0
	for _, group := range groups {
		if group.Error != nil {
			failed++
		}
	}
	if failed == len(groups) {
		return response, unavailable()
	}
	response.Partial = failed > 0
	if req.View == ViewList {
		group := groups[0]
		response.Module, response.Type, response.Items = group.Module, group.Type, group.Items
		response.Pagination = &Pagination{Page: req.Page, PageSize: req.PageSize, Total: *group.Total, HasMore: group.HasMore}
	} else {
		response.Groups = groups
	}
	return response, nil
}

func (s *Service) SearchPreview(ctx context.Context, viewer Viewer, req SearchRequest) (SearchResponse, error) {
	req.View = ViewPreview
	return s.Search(ctx, viewer, req)
}

func (s *Service) SearchGrouped(ctx context.Context, viewer Viewer, req SearchRequest) (SearchResponse, error) {
	req.View = ViewGrouped
	return s.Search(ctx, viewer, req)
}

func (s *Service) SearchList(ctx context.Context, viewer Viewer, req SearchRequest) (SearchResponse, error) {
	req.View = ViewList
	return s.Search(ctx, viewer, req)
}

func (s *Service) Capabilities(ctx context.Context, viewer Viewer, req SearchRequest) (CapabilitiesResponse, error) {
	if err := ctx.Err(); err != nil {
		return CapabilitiesResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := NormalizeSearchRequest(req)
	if err != nil {
		return CapabilitiesResponse{}, err
	}
	modules, err := s.registry.capabilities(ctx, req, viewer)
	if err != nil {
		return CapabilitiesResponse{}, err
	}
	return CapabilitiesResponse{Scope: req.Scope, Modules: modules}, nil
}

func unavailable() error {
	return apperr.New(http.StatusServiceUnavailable, "search.unavailable", "Search is temporarily unavailable", nil)
}

func (s *Service) runProviderTasks(ctx context.Context, viewer Viewer, req SearchRequest, tasks []ProviderTask) ([]SearchGroup, error) {
	groups := make([]SearchGroup, len(tasks))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(searchWorkerCount, len(tasks)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				groups[index] = s.executeProviderTask(ctx, viewer, req, tasks[index])
			}
		}()
	}
	for index := range tasks {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return nil, ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return groups, nil
}

func (s *Service) executeProviderTask(parent context.Context, viewer Viewer, req SearchRequest, task ProviderTask) (group SearchGroup) {
	started := time.Now()
	var queueWait time.Duration
	defer func() {
		status := "success"
		if group.Error != nil {
			status = group.Error.Code
		} else if len(group.Items) == 0 {
			status = "zero_result"
		}
		// 不记录查询词、SQL、用户 ID 或私有内容。
		level := slog.LevelDebug
		if group.Error != nil {
			level = slog.LevelWarn
		}
		slog.Log(parent, level, "search_provider", "module", task.Module, "type", task.Type.Key, "status", status, "duration_ms", time.Since(started).Milliseconds(), "queue_wait_ms", queueWait.Milliseconds(), "result_count", len(group.Items))
	}()
	group = SearchGroup{Module: task.Module, Type: task.Type.Key, Label: task.Type.Label, Items: []SearchHit{}}
	ctx, cancel := context.WithTimeout(parent, s.providerTimeout)
	defer cancel()
	if err := searchlimit.Acquire(ctx); err != nil {
		group.Error = providerError(err)
		return group
	}
	defer searchlimit.Release()
	queueWait = time.Since(started)
	limit := GroupedLimit
	if req.View == ViewPreview {
		limit = PreviewLimit
	}
	input := ProviderSearchRequest{Query: req.Query, Scope: req.Scope, Type: task.Type.Key, Sort: req.Sort, Filters: cloneFilters(req.Filters), Limit: limit + 1}
	if input.Sort == "" {
		input.Sort = task.Type.DefaultSort
		if req.Query == "" {
			input.Sort = task.Type.DiscoverySort
		}
	}
	if req.View == ViewList {
		limit = req.PageSize
		input.Limit, input.Offset, input.ExactTotal = limit, (req.Page-1)*req.PageSize, true
	}
	result, err := task.Provider.Search(ctx, viewer, input)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && input.ExactTotal && (result.Total == nil || *result.Total < 0) {
		err = fmt.Errorf("provider omitted exact count")
	}
	if err == nil {
		group.Items, err = normalizeHits(result.Items, task)
	}
	if err != nil {
		group.Items = []SearchHit{}
		group.Error = providerError(err)
		return group
	}
	group.HasMore = len(group.Items) > limit
	if len(group.Items) > limit {
		group.Items = group.Items[:limit]
	}
	if input.ExactTotal {
		group.Total = result.Total
		group.HasMore = int64(input.Offset+len(group.Items)) < *result.Total
	}
	return group
}

func providerError(err error) *GroupError {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &GroupError{Code: "search.provider_timeout", Message: "This search group timed out"}
	}
	// 不向客户端暴露 SQL、私有正文或底层错误详情。
	return &GroupError{Code: "search.provider_failed", Message: "This search group is unavailable"}
}

func cloneFilters(filters map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(filters))
	for key, values := range filters {
		cloned[key] = slices.Clone(values)
	}
	return cloned
}

func normalizeHits(hits []SearchHit, task ProviderTask) ([]SearchHit, error) {
	items := make([]SearchHit, 0, len(hits))
	seen := make(map[string]bool)
	for _, hit := range hits {
		hit = NormalizeSearchHit(hit)
		if err := ValidateSearchHit(hit); err != nil {
			return nil, err
		}
		if hit.Module != task.Module || hit.Type != task.Type.Key {
			return nil, fmt.Errorf("provider returned a different content type")
		}
		if seen[hit.Key] {
			continue
		}
		seen[hit.Key] = true
		metadata := make(map[string]any)
		for _, field := range task.Type.MetadataFields {
			if value, ok := hit.Metadata[field]; ok {
				metadata[field] = value
			}
		}
		hit.Metadata = metadata
		fields := make([]string, 0)
		for _, field := range hit.MatchedFields {
			if slices.Contains(task.Type.SearchFields, field) && !slices.Contains(fields, field) {
				fields = append(fields, field)
			}
		}
		hit.MatchedFields = fields
		items = append(items, hit)
	}
	return items, nil
}
