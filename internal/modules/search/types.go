package search

import (
	"fmt"
	"net/url"
	"strings"

	"atoman/internal/platform/authctx"
)

type Scope string
type View string

const (
	ScopePublic Scope = "public"
	ScopeMine   Scope = "mine"
	ViewPreview View  = "preview"
	ViewGrouped View  = "grouped"
	ViewList    View  = "list"

	DefaultPageSize = 24
	MaxPageSize     = 50
	MaxOffset       = 10000
	MaxQueryRunes   = 256
	PreviewLimit    = 2
	GroupedLimit    = 6
)

// 保留完整认证上下文；public 的数据库查询仍必须显式使用公开可见性条件。
type Viewer = authctx.CurrentUser

type SearchRequest struct {
	Query    string
	Scope    Scope
	Module   string
	Types    []string
	View     View
	Sort     string
	Page     int
	PageSize int
	Filters  map[string][]string
}

type SearchHit struct {
	Key           string         `json:"key"`
	Module        string         `json:"module"`
	Type          string         `json:"type"`
	ID            string         `json:"id"`
	Title         string         `json:"title"`
	Subtitle      string         `json:"subtitle,omitempty"`
	Snippet       string         `json:"snippet,omitempty"`
	CoverURL      string         `json:"cover_url,omitempty"`
	Href          string         `json:"href"`
	MatchedFields []string       `json:"matched_fields,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

type GroupError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SearchGroup struct {
	Module  string      `json:"module"`
	Type    string      `json:"type"`
	Label   string      `json:"label"`
	Items   []SearchHit `json:"items"`
	Total   *int64      `json:"total" extensions:"x-nullable"`
	HasMore bool        `json:"has_more"`
	Error   *GroupError `json:"error" extensions:"x-nullable"`
}

type Pagination struct {
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
	HasMore  bool  `json:"has_more"`
}

type SearchResponse struct {
	View       View          `json:"view"`
	Scope      Scope         `json:"scope"`
	Query      string        `json:"query"`
	Module     string        `json:"module,omitempty"`
	Type       string        `json:"type,omitempty"`
	Groups     []SearchGroup `json:"groups"`
	Items      []SearchHit   `json:"items"`
	Pagination *Pagination   `json:"pagination,omitempty"`
	Partial    bool          `json:"partial"`
}

type SortDefinition struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type FilterOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type FilterDefinition struct {
	Key      string         `json:"key"`
	Label    string         `json:"label"`
	Kind     string         `json:"kind"` // text / enum / entity(UUID) / date / number / boolean
	Multiple bool           `json:"multiple"`
	Integer  bool           `json:"integer,omitempty"` // number 筛选的整数约束，例如出版年份。
	Options  []FilterOption `json:"options,omitempty"`
	Min      *float64       `json:"min,omitempty"`
	Max      *float64       `json:"max,omitempty"`
}

type TypeCapability struct {
	Key             string             `json:"key"`
	Label           string             `json:"label"`
	Sorts           []SortDefinition   `json:"sorts"`
	Filters         []FilterDefinition `json:"filters"`
	DefaultSort     string             `json:"default_sort"`
	DiscoverySort   string             `json:"discovery_sort,omitempty"`
	AllowEmptyQuery bool               `json:"allow_empty_query"`
	// Provider 声明可输出的字段。未声明的元数据/命中字段在协议边界移除。
	MetadataFields []string `json:"-"`
	SearchFields   []string `json:"search_fields,omitempty"`
}

type ModuleCapabilities struct {
	Module          string             `json:"module"`
	Label           string             `json:"label"`
	Scope           Scope              `json:"scope"`
	Types           []TypeCapability   `json:"types"`
	Sorts           []SortDefinition   `json:"sorts"`
	Filters         []FilterDefinition `json:"filters"`
	DefaultSort     string             `json:"default_sort"`
	AllowEmptyQuery bool               `json:"allow_empty_query"`
	AccessModule    string             `json:"-"` // 例如 users 搜索域复用 blog 的站点开关。
}

type CapabilitiesResponse struct {
	Scope   Scope                `json:"scope"`
	Modules []ModuleCapabilities `json:"modules"`
}

type ProviderSearchRequest struct {
	Query      string
	Scope      Scope
	Type       string
	Sort       string
	Filters    map[string][]string
	Limit      int
	Offset     int
	ExactTotal bool
}

type ProviderSearchResult struct {
	Items []SearchHit
	Total *int64 // 仅 ExactTotal=true 时必须计算，且应与 items 使用完全相同的权限条件。
}

func BuildSearchKey(module, entityType, id string) string {
	return module + ":" + entityType + ":" + id
}

func NormalizeSearchHit(hit SearchHit) SearchHit {
	hit.Module, hit.Type, hit.ID = strings.TrimSpace(hit.Module), strings.TrimSpace(hit.Type), strings.TrimSpace(hit.ID)
	hit.Key = BuildSearchKey(hit.Module, hit.Type, hit.ID)
	hit.Title, hit.Subtitle, hit.Snippet = strings.TrimSpace(hit.Title), strings.TrimSpace(hit.Subtitle), strings.TrimSpace(hit.Snippet)
	return hit
}

func ValidateSearchHit(hit SearchHit) error {
	if !validIdentifier(hit.Module) || !validIdentifier(hit.Type) || hit.ID == "" || hit.Title == "" || hit.Key != BuildSearchKey(hit.Module, hit.Type, hit.ID) {
		return fmt.Errorf("invalid search hit identity")
	}
	u, err := url.Parse(hit.Href)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") || strings.ContainsAny(u.Path, "\\\r\n\t") {
		return fmt.Errorf("search hit href must be a local absolute path")
	}
	return nil
}
