package search

import (
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"atoman/internal/platform/apperr"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func validIdentifier(value string) bool { return identifierPattern.MatchString(value) }

func invalid(field, reason string) error {
	return apperr.New(http.StatusBadRequest, "search.invalid_parameter", reason, map[string]any{"parameter": field})
}

func ParseSearchRequest(c *gin.Context) (SearchRequest, error) {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return SearchRequest{}, invalid("query", "Invalid query encoding")
	}
	filters, err := ParseFilterValues(values)
	if err != nil {
		return SearchRequest{}, err
	}
	known := []string{"q", "scope", "module", "type", "view", "sort", "page", "page_size"}
	for key, vals := range values {
		if strings.HasPrefix(key, "filter[") {
			continue
		}
		if !slices.Contains(known, key) {
			return SearchRequest{}, invalid(key, "Unknown search parameter")
		}
		if key != "type" && len(vals) != 1 {
			return SearchRequest{}, invalid(key, "Parameter must occur once")
		}
	}
	page, size := 0, 0
	for key, dest := range map[string]*int{"page": &page, "page_size": &size} {
		if values.Has(key) {
			value, parseErr := strconv.Atoi(values.Get(key))
			if parseErr != nil || value < 1 {
				return SearchRequest{}, invalid(key, "Expected a positive integer")
			}
			*dest = value
		}
	}
	return NormalizeSearchRequest(SearchRequest{
		Query: values.Get("q"), Scope: Scope(values.Get("scope")), Module: values.Get("module"),
		Types: values["type"], View: View(values.Get("view")), Sort: values.Get("sort"),
		Page: page, PageSize: size, Filters: filters,
	})
}

func NormalizeSearchQuery(query string) string { return strings.Join(strings.Fields(query), " ") }

func NormalizeSearchRequest(req SearchRequest) (SearchRequest, error) {
	if !utf8.ValidString(req.Query) || utf8.RuneCountInString(req.Query) > MaxQueryRunes {
		return req, invalid("q", "Query must contain at most 256 Unicode characters")
	}
	req.Query = NormalizeSearchQuery(req.Query)
	if req.Scope == "" {
		req.Scope = ScopePublic
	}
	if req.Scope != ScopePublic && req.Scope != ScopeMine {
		return req, invalid("scope", "Unsupported search scope")
	}
	if req.View == "" {
		req.View = ViewGrouped
	}
	if req.View != ViewPreview && req.View != ViewGrouped && req.View != ViewList {
		return req, invalid("view", "Unsupported search view")
	}
	if req.Module != "" && !validIdentifier(req.Module) {
		return req, invalid("module", "Invalid module")
	}
	if req.Sort != "" && !validIdentifier(req.Sort) {
		return req, invalid("sort", "Invalid sort")
	}
	types := make([]string, 0, len(req.Types))
	for _, kind := range req.Types {
		if !validIdentifier(kind) {
			return req, invalid("type", "Invalid content type")
		}
		if !slices.Contains(types, kind) {
			types = append(types, kind)
		}
	}
	req.Types = types
	var err error
	req.Page, req.PageSize, err = ValidatePagination(req.Page, req.PageSize)
	if err != nil {
		return req, err
	}
	if req.View == ViewList && (req.Module == "" || len(req.Types) != 1) {
		return req, invalid("type", "List view requires one module and one type")
	}
	if req.View != ViewList && req.Page != 1 {
		return req, invalid("page", "Only list view supports page numbers")
	}
	filters := make(map[string][]string, len(req.Filters))
	for key, values := range req.Filters {
		if !validIdentifier(key) || len(values) == 0 || len(values) > 50 {
			return req, invalid("filter["+key+"]", "Invalid filter")
		}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" || utf8.RuneCountInString(value) > MaxQueryRunes {
				return req, invalid("filter["+key+"]", "Invalid filter value")
			}
			if !slices.Contains(filters[key], value) {
				filters[key] = append(filters[key], value)
			}
		}
	}
	req.Filters = filters
	return req, nil
}

func ParseFilterValues(values url.Values) (map[string][]string, error) {
	filters := make(map[string][]string)
	for key, vals := range values {
		if !strings.HasPrefix(key, "filter[") {
			continue
		}
		if !strings.HasSuffix(key, "]") {
			return nil, invalid(key, "Malformed filter")
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "filter["), "]")
		if !validIdentifier(name) {
			return nil, invalid(key, "Malformed filter")
		}
		filters[name] = vals
	}
	return filters, nil
}

func ValidatePagination(page, size int) (int, int, error) {
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = DefaultPageSize
	}
	if page < 1 {
		return 0, 0, invalid("page", "Page must be positive")
	}
	if size < 1 || size > MaxPageSize {
		return 0, 0, invalid("page_size", "Page size must be between 1 and 50")
	}
	// 除法先行，避免用户提供的巨大页码在计算 offset 时溢出。
	if page-1 > MaxOffset/size {
		return 0, 0, invalid("page", "Search pagination is limited to offset 10000")
	}
	return page, size, nil
}

func ValidateScope(scope Scope, viewer Viewer) error {
	if scope != ScopePublic && scope != ScopeMine {
		return invalid("scope", "Unsupported search scope")
	}
	if scope == ScopeMine && viewer.ID == uuid.Nil {
		return apperr.Unauthorized("Login required for personal search")
	}
	return nil
}

// caps 已由 Registry 选定；对每个类型验证，自然实现多类型能力交集。
func ValidateSearchRequest(req SearchRequest, caps []ModuleCapabilities) error {
	types := make([]TypeCapability, 0)
	for _, module := range caps {
		for _, kind := range module.Types {
			types = append(types, kind)
			if req.Query == "" && req.Module != "" && len(req.Types) > 0 && !kind.AllowEmptyQuery {
				return invalid("q", "This type requires a query")
			}
			if req.Sort != "" && !hasSort(kind.Sorts, req.Sort) {
				return invalid("sort", "Sort is not supported by all selected types")
			}
		}
	}
	if len(types) == 0 && (len(req.Filters) > 0 || req.Sort != "") {
		return invalid("filter", "No searchable type supports these conditions")
	}
	if len(types) > 0 {
		_, filters := commonCapabilities(types)
		for key, values := range req.Filters {
			index := slices.IndexFunc(filters, func(filter FilterDefinition) bool { return filter.Key == key })
			if index < 0 {
				return invalid("filter["+key+"]", "Filter is not supported by all selected types")
			}
			if err := validateFilter(filters[index], values); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasSort(sorts []SortDefinition, key string) bool {
	return slices.ContainsFunc(sorts, func(sort SortDefinition) bool { return sort.Key == key })
}

func validateFilter(filter FilterDefinition, values []string) error {
	field := "filter[" + filter.Key + "]"
	if !filter.Multiple && len(values) > 1 {
		return invalid(field, "Filter does not support multiple values")
	}
	for _, value := range values {
		valid := true
		switch filter.Kind {
		case "text":
		case "entity":
			id, err := uuid.Parse(value)
			valid = err == nil && id != uuid.Nil
		case "date":
			_, err := time.Parse(time.DateOnly, value)
			valid = err == nil
		case "number":
			n, err := strconv.ParseFloat(value, 64)
			valid = err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && (filter.Min == nil || n >= *filter.Min) && (filter.Max == nil || n <= *filter.Max)
			valid = valid && (!filter.Integer || n == math.Trunc(n))
		case "boolean":
			valid = value == "true" || value == "false"
		case "enum":
			valid = slices.ContainsFunc(filter.Options, func(option FilterOption) bool { return option.Value == value })
		default:
			valid = false
		}
		if !valid {
			return invalid(field, "Invalid filter value")
		}
	}
	return nil
}
