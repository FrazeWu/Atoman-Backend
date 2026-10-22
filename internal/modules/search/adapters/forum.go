package adapters

import (
	"context"

	"atoman/internal/modules/forum"
	"atoman/internal/modules/search"
	"atoman/internal/platform/apperr"
)

type ForumProvider struct{ repo *forum.Repo }

func NewForumProvider(repo *forum.Repo) *ForumProvider { return &ForumProvider{repo: repo} }
func (p *ForumProvider) Module() string                { return "forum" }

func (p *ForumProvider) Capabilities(_ context.Context, _ search.Viewer, scope search.Scope) search.ModuleCapabilities {
	caps := search.ModuleCapabilities{Module: p.Module(), Label: "论坛", Scope: scope}
	if scope != search.ScopePublic {
		return caps
	}
	caps.Types = []search.TypeCapability{{Key: "topic", Label: "主题", DefaultSort: "relevance",
		Sorts:        []search.SortDefinition{{Key: "relevance", Label: "相关度"}, {Key: "recent", Label: "最新"}},
		Filters:      []search.FilterDefinition{{Key: "category_id", Label: "板块", Kind: "entity", Multiple: true}, {Key: "tag", Label: "标签", Kind: "text", Multiple: true}},
		SearchFields: []string{"title", "content", "tags", "category", "author", "reply", "reply_author"}, MetadataFields: []string{"category_id", "category_name", "tags"}}}
	return caps
}

func (p *ForumProvider) Search(ctx context.Context, _ search.Viewer, req search.ProviderSearchRequest) (search.ProviderSearchResult, error) {
	out := search.ProviderSearchResult{Items: []search.SearchHit{}}
	if req.Scope != search.ScopePublic {
		return out, apperr.BadRequest("search.invalid_scope", "Forum private search is not available yet")
	}
	if req.Type != "topic" {
		return out, apperr.BadRequest("search.invalid_type", "Unknown forum search type")
	}
	categories, err := entityFilter(req.Filters["category_id"])
	if err != nil {
		return out, err
	}
	items, total, err := p.repo.SearchPublicTopics(ctx, forum.TopicSearchOptions{Query: req.Query, Sort: req.Sort, CategoryIDs: categories, Tags: req.Filters["tag"], Limit: req.Limit, Offset: req.Offset, ExactTotal: req.ExactTotal})
	if err != nil {
		return out, err
	}
	if req.ExactTotal {
		out.Total = &total
	}
	for _, item := range items {
		hit := search.SearchHit{Module: "forum", Type: "topic", ID: item.ID.String(), Title: item.Title, Snippet: searchSnippet(item.Content), Href: "/forum/topic/" + item.ID.String(), Metadata: map[string]any{"category_id": item.CategoryID.String(), "tags": item.Tags}}
		if item.Category != nil {
			hit.Metadata["category_name"] = item.Category.Name
		}
		if item.User != nil {
			hit.Subtitle = item.User.DisplayName
		}
		out.Items = append(out.Items, hit)
	}
	return out, nil
}
