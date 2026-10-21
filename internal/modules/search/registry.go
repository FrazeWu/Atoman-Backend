package search

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"atoman/internal/platform/apperr"
)

// 每个请求加载一次站点访问快照；nil 仅用于没有站点配置的独立调用/测试。
type ModuleAccess func(context.Context) (map[string]bool, error)

type Registry struct {
	providers []SearchProvider
	byModule  map[string]SearchProvider
	access    ModuleAccess
}

type ProviderTask struct {
	Provider SearchProvider
	Module   string
	Type     TypeCapability
}

func NewRegistry(access ...ModuleAccess) *Registry {
	r := &Registry{byModule: make(map[string]SearchProvider)}
	if len(access) > 0 {
		r.access = access[0]
	}
	return r
}

func (r *Registry) Register(provider SearchProvider) error {
	if provider == nil || !validIdentifier(provider.Module()) {
		return fmt.Errorf("invalid search provider")
	}
	if _, exists := r.byModule[provider.Module()]; exists {
		return fmt.Errorf("search provider %q already registered", provider.Module())
	}
	r.byModule[provider.Module()] = provider
	r.providers = append(r.providers, provider)
	return nil
}

func (r *Registry) Get(module string) (SearchProvider, bool) {
	provider, ok := r.byModule[module]
	return provider, ok
}

func (r *Registry) Modules() []string {
	modules := make([]string, 0, len(r.providers))
	for _, provider := range r.providers {
		modules = append(modules, provider.Module())
	}
	return modules
}

func (r *Registry) capabilities(ctx context.Context, req SearchRequest, viewer Viewer) ([]ModuleCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateScope(req.Scope, viewer); err != nil {
		return nil, err
	}
	if req.Module != "" {
		if _, ok := r.Get(req.Module); !ok {
			return nil, invalid("module", "Unknown search module")
		}
	}
	var access map[string]bool
	if r.access != nil {
		var err error
		access, err = r.access(ctx)
		if err != nil {
			return nil, apperr.Internal(err)
		}
	}
	modules := make([]ModuleCapabilities, 0)
	matchedTypes := make(map[string]bool)
	for _, provider := range r.providers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if req.Module != "" && provider.Module() != req.Module {
			continue
		}
		caps := provider.Capabilities(ctx, viewer, req.Scope)
		caps.Module, caps.Scope = provider.Module(), req.Scope
		accessModule := caps.AccessModule
		if accessModule == "" {
			accessModule = caps.Module
		}
		if r.access != nil && !access[accessModule] {
			if req.Module != "" {
				return nil, apperr.Forbidden("site.module_disabled", "This module is not available")
			}
			continue
		}
		selected := make([]TypeCapability, 0, len(caps.Types))
		seen := make(map[string]bool)
		for _, kind := range caps.Types {
			if err := validateTypeCapability(kind); err != nil {
				return nil, apperr.Internal(err)
			}
			if seen[kind.Key] {
				return nil, apperr.Internal(fmt.Errorf("duplicate search type"))
			}
			seen[kind.Key] = true
			if len(req.Types) == 0 || slices.Contains(req.Types, kind.Key) {
				// 不修改 Provider 持有的切片，避免并发请求之间污染能力声明。
				if kind.Filters == nil {
					kind.Filters = []FilterDefinition{}
				}
				selected = append(selected, kind)
				matchedTypes[kind.Key] = true
			}
		}
		if len(selected) == 0 {
			continue
		}
		caps.Types = selected
		caps.Sorts, caps.Filters = commonCapabilities(selected)
		caps.DefaultSort = ""
		if hasSort(caps.Sorts, selected[0].DefaultSort) {
			caps.DefaultSort = selected[0].DefaultSort
		}
		caps.AllowEmptyQuery = true
		for _, kind := range selected {
			caps.AllowEmptyQuery = caps.AllowEmptyQuery && kind.AllowEmptyQuery
		}
		modules = append(modules, caps)
	}
	for _, kind := range req.Types {
		if !matchedTypes[kind] {
			return nil, invalid("type", "Content type is not available in this scope")
		}
	}
	if req.Module != "" && len(modules) == 0 {
		return nil, invalid("scope", "Module has no searchable types in this scope")
	}
	return modules, nil
}

func (r *Registry) Resolve(ctx context.Context, req SearchRequest, viewer Viewer) ([]ProviderTask, error) {
	caps, err := r.capabilities(ctx, req, viewer)
	if err != nil {
		return nil, err
	}
	if err := ValidateSearchRequest(req, caps); err != nil {
		return nil, err
	}
	tasks := make([]ProviderTask, 0)
	for _, module := range caps {
		for _, kind := range module.Types {
			tasks = append(tasks, ProviderTask{Provider: r.byModule[module.Module], Module: module.Module, Type: kind})
		}
	}
	return tasks, nil
}

func validateTypeCapability(kind TypeCapability) error {
	if !validIdentifier(kind.Key) || kind.Label == "" || !hasSort(kind.Sorts, kind.DefaultSort) ||
		(kind.AllowEmptyQuery && !hasSort(kind.Sorts, kind.DiscoverySort)) {
		return fmt.Errorf("invalid search type capability")
	}
	seen := make(map[string]bool)
	for _, sort := range kind.Sorts {
		if !validIdentifier(sort.Key) || seen[sort.Key] {
			return fmt.Errorf("invalid search sort capability")
		}
		seen[sort.Key] = true
	}
	seen = make(map[string]bool)
	for _, filter := range kind.Filters {
		if !validIdentifier(filter.Key) || seen[filter.Key] || !slices.Contains([]string{"text", "enum", "entity", "date", "number", "boolean"}, filter.Kind) {
			return fmt.Errorf("invalid search filter capability")
		}
		seen[filter.Key] = true
	}
	return nil
}

func commonCapabilities(types []TypeCapability) ([]SortDefinition, []FilterDefinition) {
	sorts := append([]SortDefinition{}, types[0].Sorts...)
	filters := append([]FilterDefinition{}, types[0].Filters...)
	for _, kind := range types[1:] {
		sorts = slices.DeleteFunc(sorts, func(sort SortDefinition) bool { return !hasSort(kind.Sorts, sort.Key) })
		filters = slices.DeleteFunc(filters, func(filter FilterDefinition) bool {
			return !slices.ContainsFunc(kind.Filters, func(other FilterDefinition) bool { return reflect.DeepEqual(filter, other) })
		})
	}
	return sorts, filters
}
