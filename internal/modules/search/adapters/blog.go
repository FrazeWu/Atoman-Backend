package adapters

import (
	"context"
	"net/url"
	"strings"

	"atoman/internal/modules/blog"
	"atoman/internal/modules/search"
	"atoman/internal/modules/shortnote"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
)

type BlogProvider struct {
	service *blog.Service
	notes   *shortnote.Service
}

func NewBlogProvider(service *blog.Service, notes *shortnote.Service) *BlogProvider {
	return &BlogProvider{service: service, notes: notes}
}
func (p *BlogProvider) Module() string { return "blog" }

func (p *BlogProvider) Capabilities(_ context.Context, _ search.Viewer, scope search.Scope) search.ModuleCapabilities {
	caps := search.ModuleCapabilities{Module: p.Module(), Label: "博客", Scope: scope}
	if scope != search.ScopePublic {
		return caps
	}
	sorts := []search.SortDefinition{{Key: "relevance", Label: "相关度"}, {Key: "recent", Label: "最新"}}
	author := search.FilterDefinition{Key: "author_id", Label: "作者", Kind: "entity", Multiple: true}
	caps.Types = []search.TypeCapability{
		{Key: "post", Label: "文章", Sorts: sorts, DefaultSort: "relevance",
			Filters: []search.FilterDefinition{author,
				{Key: "channel_id", Label: "频道", Kind: "entity", Multiple: true},
				{Key: "collection_id", Label: "合集", Kind: "entity", Multiple: true},
				{Key: "tag", Label: "标签", Kind: "text", Multiple: true}},
			SearchFields: []string{"title", "summary", "content"}, MetadataFields: []string{"published_at"}},
		{Key: "short_note", Label: "短笺", Sorts: sorts, DefaultSort: "relevance", Filters: []search.FilterDefinition{author}, SearchFields: []string{"content"}},
		{Key: "channel", Label: "频道", Sorts: sorts, DefaultSort: "relevance", Filters: []search.FilterDefinition{}, SearchFields: []string{"name", "description"}},
		{Key: "collection", Label: "合集", Sorts: sorts, DefaultSort: "relevance", Filters: []search.FilterDefinition{}, SearchFields: []string{"name", "description"}},
	}
	return caps
}

func (p *BlogProvider) Search(ctx context.Context, _ search.Viewer, req search.ProviderSearchRequest) (search.ProviderSearchResult, error) {
	out := search.ProviderSearchResult{Items: []search.SearchHit{}}
	if req.Scope != search.ScopePublic {
		return out, apperr.BadRequest("search.invalid_scope", "Blog private search is not available yet")
	}
	var total int64
	var err error
	switch req.Type {
	case "post":
		authors, e := entityFilter(req.Filters["author_id"])
		if e != nil {
			return out, e
		}
		channels, e := entityFilter(req.Filters["channel_id"])
		if e != nil {
			return out, e
		}
		collections, e := entityFilter(req.Filters["collection_id"])
		if e != nil {
			return out, e
		}
		tags := make([]string, 0, len(req.Filters["tag"]))
		for _, tag := range req.Filters["tag"] {
			tags = append(tags, strings.ToLower(strings.Join(strings.Fields(tag), " ")))
		}
		var items []blog.BlogSearchResultDTO
		items, total, err = p.service.SearchPublishedBlogContentsPage(ctx, blog.BlogSearchQuery{Text: req.Query, Sort: req.Sort,
			AuthorIDs: authors, ChannelIDs: channels, CollectionIDs: collections, Tags: tags}, req.Limit, req.Offset, req.ExactTotal)
		for _, item := range items {
			hit := search.SearchHit{Module: "blog", Type: "post", ID: item.ID.String(), Title: item.Title,
				Snippet: searchSnippet(item.Snippet), CoverURL: item.CoverURL, Href: item.TargetPath,
				MatchedFields: []string{item.MatchField}, Metadata: map[string]any{"published_at": item.PublishedAt}}
			if item.User != nil {
				hit.Subtitle = item.User.DisplayName
			}
			out.Items = append(out.Items, hit)
		}
	case "short_note":
		authors, e := entityFilter(req.Filters["author_id"])
		if e != nil {
			return out, e
		}
		items, count, e := p.notes.SearchPublic(ctx, shortnote.PublicSearchOptions{Query: req.Query, Sort: req.Sort, AuthorIDs: authors, Limit: req.Limit, Offset: req.Offset, ExactTotal: req.ExactTotal})
		total, err = count, e
		for _, item := range items {
			hit := search.SearchHit{Module: "blog", Type: "short_note", ID: item.ID.String(), Title: searchSnippet(item.Content), Snippet: searchSnippet(item.Content), Href: "/posts/notes/" + item.ID.String(), MatchedFields: []string{"content"}}
			if item.User != nil {
				hit.Subtitle = item.User.DisplayName
			}
			out.Items = append(out.Items, hit)
		}
	case "channel":
		items, count, e := p.service.SearchPublicChannels(ctx, blog.ContainerSearchOptions{Query: req.Query, Sort: req.Sort, Limit: req.Limit, Offset: req.Offset, ExactTotal: req.ExactTotal})
		total, err = count, e
		for _, item := range items {
			out.Items = append(out.Items, search.SearchHit{Module: "blog", Type: "channel", ID: item.ID.String(), Title: item.Name, Snippet: searchSnippet(item.Description), CoverURL: item.CoverURL, Href: "/channels/" + url.PathEscape(item.Slug)})
		}
	case "collection":
		items, count, e := p.service.SearchPublicCollections(ctx, blog.ContainerSearchOptions{Query: req.Query, Sort: req.Sort, Limit: req.Limit, Offset: req.Offset, ExactTotal: req.ExactTotal})
		total, err = count, e
		for _, item := range items {
			out.Items = append(out.Items, search.SearchHit{Module: "blog", Type: "collection", ID: item.ID.String(), Title: item.Name, Snippet: searchSnippet(item.Description), CoverURL: item.CoverURL, Href: "/posts/collection/" + item.ID.String()})
		}
	default:
		return out, apperr.BadRequest("search.invalid_type", "Unknown blog search type")
	}
	if req.ExactTotal {
		out.Total = &total
	}
	return out, err
}

func entityFilter(values []string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(value)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}
