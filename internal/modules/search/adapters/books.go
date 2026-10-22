package adapters

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"atoman/internal/modules/books"
	"atoman/internal/modules/search"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
)

type BooksProvider struct{ service *books.Service }

func NewBooksProvider(service *books.Service) *BooksProvider { return &BooksProvider{service: service} }
func (p *BooksProvider) Module() string                      { return "books" }

func (p *BooksProvider) Capabilities(_ context.Context, viewer search.Viewer, scope search.Scope) search.ModuleCapabilities {
	caps := search.ModuleCapabilities{Module: p.Module(), Label: "图书", Scope: scope}
	if scope == search.ScopeMine && viewer.ID != uuid.Nil {
		for _, kind := range []struct {
			key, label string
			fields     []string
		}{
			{"note", "私人笔记", []string{"title", "body"}},
			{"book_list", "我创建的书单", []string{"title", "description"}},
			{"library", "我的书架", []string{"title", "description"}},
			{"bookmark", "收藏书单", []string{"title", "description"}},
		} {
			caps.Types = append(caps.Types, privateTypeCapability(kind.key, kind.label, kind.fields))
		}
		return caps
	}
	if scope != search.ScopePublic {
		return caps
	}
	sorts := []search.SortDefinition{{Key: "relevance", Label: "相关度"}, {Key: "title", Label: "名称"}, {Key: "newest", Label: "最新"}}
	minYear, maxYear := 1.0, 9999.0
	caps.Types = []search.TypeCapability{
		{Key: "work", Label: "作品", DefaultSort: "relevance", DiscoverySort: "title", AllowEmptyQuery: true,
			Sorts: append(append([]search.SortDefinition{}, sorts...), search.SortDefinition{Key: "rating_desc", Label: "评分（至少 6 条有效评分）"}),
			Filters: []search.FilterDefinition{
				{Key: "language", Label: "作品语言", Kind: "text", Multiple: true},
				{Key: "author_id", Label: "作者", Kind: "entity", Multiple: true},
				{Key: "year_from", Label: "出版年份起", Kind: "number", Integer: true, Min: &minYear, Max: &maxYear},
				{Key: "year_to", Label: "出版年份止", Kind: "number", Integer: true, Min: &minYear, Max: &maxYear},
			}, SearchFields: []string{"title", "original_title", "subtitle", "author"},
			MetadataFields: []string{"authors", "language", "rating_score", "weighted_rating_score", "rating_count", "rating_active", "minimum_rating_count"}},
		{Key: "author", Label: "作者", DefaultSort: "relevance", DiscoverySort: "title", AllowEmptyQuery: true,
			Sorts: sorts, SearchFields: []string{"name"}, MetadataFields: []string{"work_count"}},
		{Key: "book_list", Label: "书单", DefaultSort: "relevance", DiscoverySort: "newest", AllowEmptyQuery: true,
			Sorts: sorts, SearchFields: []string{"title", "description"}, MetadataFields: []string{"owner_id", "owner_name", "item_count", "published_at"}},
	}
	return caps
}

func (p *BooksProvider) Search(ctx context.Context, viewer search.Viewer, req search.ProviderSearchRequest) (search.ProviderSearchResult, error) {
	if req.Scope == search.ScopeMine {
		data, err := p.service.SearchPrivateBooks(ctx, viewer, req.Type, books.BookSearchOptions{Query: req.Query, Sort: req.Sort, Limit: req.Limit, Offset: req.Offset, ExactTotal: req.ExactTotal})
		out := search.ProviderSearchResult{Items: []search.SearchHit{}, Total: data.Total}
		for _, item := range data.Items {
			href := "/books/lists/" + item.ID.String()
			switch req.Type {
			case "note":
				href = "/books/notes?work=" + item.WorkID.String()
			case "library":
				href = "/books/work/" + item.WorkID.String()
			case "book_list":
				href += "/edit"
			}
			out.Items = append(out.Items, search.SearchHit{Module: p.Module(), Type: req.Type, ID: item.ID.String(), Title: item.Title, Snippet: searchSnippet(item.Snippet), Href: href})
		}
		return out, err
	}
	if req.Scope != search.ScopePublic {
		return search.ProviderSearchResult{}, apperr.BadRequest("search.invalid_scope", "Unsupported books search scope")
	}
	opts := books.BookSearchOptions{Query: req.Query, Sort: req.Sort, Limit: req.Limit, Offset: req.Offset, ExactTotal: req.ExactTotal}
	switch req.Type {
	case "work":
		workOpts := books.WorkSearchOptions{BookSearchOptions: opts, Languages: req.Filters["language"]}
		for _, raw := range req.Filters["author_id"] {
			id, err := uuid.Parse(raw)
			if err != nil {
				return search.ProviderSearchResult{}, err
			}
			workOpts.AuthorIDs = append(workOpts.AuthorIDs, id)
		}
		workOpts.YearFrom, workOpts.YearTo = yearFilter(req.Filters["year_from"]), yearFilter(req.Filters["year_to"])
		result, err := p.service.SearchWorksAdvanced(ctx, workOpts)
		out := search.ProviderSearchResult{Items: []search.SearchHit{}, Total: result.Total}
		for _, item := range result.Items {
			out.Items = append(out.Items, mapBookWorkHit(item))
		}
		return out, err
	case "author":
		result, err := p.service.SearchAuthorsAdvanced(ctx, opts)
		out := search.ProviderSearchResult{Items: []search.SearchHit{}, Total: result.Total}
		for _, item := range result.Items {
			out.Items = append(out.Items, mapBookAuthorHit(item))
		}
		return out, err
	case "book_list":
		result, err := p.service.SearchPublicBookListsAdvanced(ctx, opts)
		out := search.ProviderSearchResult{Items: []search.SearchHit{}, Total: result.Total}
		for _, item := range result.Items {
			out.Items = append(out.Items, mapBookListHit(item))
		}
		return out, err
	default:
		return search.ProviderSearchResult{}, apperr.BadRequest("search.invalid_type", "Unknown books search type")
	}
}

func yearFilter(values []string) int {
	if len(values) == 0 {
		return 0
	}
	// 协议层已验证有限整数和范围；ParseFloat 兼容合法的 2026.0 / 2.026e3。
	year, _ := strconv.ParseFloat(values[0], 64)
	return int(year)
}

func mapBookWorkHit(item books.BookWorkSearchItem) search.SearchHit {
	names := make([]string, 0, len(item.Authors))
	for _, author := range item.Authors {
		names = append(names, author.Name)
	}
	return search.SearchHit{Module: "books", Type: "work", ID: item.ID.String(), Title: item.Title,
		Subtitle: strings.Join(names, "、"), Snippet: searchSnippet(item.Description), CoverURL: item.CoverURL,
		Href: "/books/work/" + item.ID.String(), Metadata: map[string]any{
			"authors": item.Authors, "language": item.Language, "rating_score": item.RatingScore,
			"weighted_rating_score": item.WeightedRatingScore, "rating_count": item.RatingCount,
			"rating_active": item.RatingActive, "minimum_rating_count": item.MinimumRatingCount,
		}}
}

func mapBookAuthorHit(item books.BookAuthorSearchItem) search.SearchHit {
	query := url.Values{"module": {"books"}, "type": {"work"}, "view": {"list"}, "filter[author_id]": {item.ID.String()}}
	return search.SearchHit{Module: "books", Type: "author", ID: item.ID.String(), Title: item.Name,
		Snippet: searchSnippet(item.Description), Href: "/books/search?" + query.Encode(), Metadata: map[string]any{"work_count": item.WorkCount}}
}

func mapBookListHit(item books.PublicBookListSearchItem) search.SearchHit {
	return search.SearchHit{Module: "books", Type: "book_list", ID: item.ID.String(), Title: item.Title,
		Subtitle: item.OwnerName, Snippet: searchSnippet(item.Description), Href: "/books/lists/" + item.ID.String(),
		Metadata: map[string]any{"owner_id": item.OwnerID.String(), "owner_name": item.OwnerName, "item_count": item.ItemCount, "published_at": item.PublishedAt}}
}

func searchSnippet(value string) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > 240 {
		return string(runes[:240]) + "…"
	}
	return string(runes)
}
