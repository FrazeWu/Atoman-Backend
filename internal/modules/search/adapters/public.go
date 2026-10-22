package adapters

import (
	"context"
	"fmt"
	"strings"

	contentmodule "atoman/internal/modules/content"
	"atoman/internal/modules/search"
	"atoman/internal/platform/apperr"
	"gorm.io/gorm"
)

// PublicProvider exposes only anonymous, publicly visible content. Each query
// owns its visibility predicate so an authenticated viewer cannot widen it.
type PublicProvider struct {
	db     *gorm.DB
	module string
	label  string
	types  []search.TypeCapability
}

func NewPublicProvider(db *gorm.DB, module, label string, kinds ...search.TypeCapability) *PublicProvider {
	return &PublicProvider{db: db, module: module, label: label, types: kinds}
}

func PublicType(key, label string, fields ...string) search.TypeCapability {
	return search.TypeCapability{Key: key, Label: label, DefaultSort: "relevance",
		Sorts:   []search.SortDefinition{{Key: "relevance", Label: "相关度"}, {Key: "recent", Label: "最新"}},
		Filters: []search.FilterDefinition{}, SearchFields: fields, MetadataFields: []string{}}
}

func (p *PublicProvider) Module() string { return p.module }

func (p *PublicProvider) Capabilities(_ context.Context, _ search.Viewer, scope search.Scope) search.ModuleCapabilities {
	caps := search.ModuleCapabilities{Module: p.module, Label: p.label, Scope: scope}
	if scope == search.ScopePublic {
		caps.Types = p.types
	}
	return caps
}

type publicRow struct {
	ID, Title, Subtitle, Snippet, CoverURL, Href, MatchField string
	CreatedAt                                                interface{}
}

// SearchPublicRows is shared only for pagination and mapping; SQL and the
// visibility predicates remain defined per content type below.
func (p *PublicProvider) Search(ctx context.Context, _ search.Viewer, req search.ProviderSearchRequest) (search.ProviderSearchResult, error) {
	if req.Scope != search.ScopePublic {
		return search.ProviderSearchResult{}, apperr.BadRequest("search.invalid_scope", "Private search is not available yet")
	}
	query := strings.TrimSpace(req.Query)
	var rows []publicRow
	base := p.db.WithContext(ctx).Table(" ")
	switch p.module + ":" + req.Type {
	case "feed:source":
		base = p.db.WithContext(ctx).Table("feed_sources AS f").Select(`f.id::text AS id, COALESCE(NULLIF(f.title,''), f.site_url) AS title, '' AS subtitle, '' AS snippet, COALESCE(f.cover_url,'') AS cover_url, '/feed?source_id=' || f.id::text AS href, f.updated_at AS created_at, 'title' AS match_field`).
			Where("f.deleted_at IS NULL AND f.hidden = false AND f.source_type = 'external_rss' AND EXISTS (SELECT 1 FROM feed_items i WHERE i.feed_source_id = f.id AND i.deleted_at IS NULL)")
	case "feed:article":
		base = p.db.WithContext(ctx).Table("feed_items AS i").Select(`i.id::text AS id, COALESCE(i.title,'') AS title, COALESCE(f.title,'') AS subtitle, COALESCE(i.summary,'') AS snippet, COALESCE(i.image_url,'') AS cover_url, '/feed/item/' || i.id::text AS href, i.published_at AS created_at, 'title' AS match_field`).
			Joins("JOIN feed_sources f ON f.id = i.feed_source_id").Where("i.deleted_at IS NULL AND f.deleted_at IS NULL AND f.hidden = false AND f.source_type = 'external_rss'")
	case "video:video":
		base = p.db.WithContext(ctx).Table("(?) AS v", contentmodule.VideoQuery(p.db.WithContext(ctx))).Select(`v.video_id::text AS id, v.title, COALESCE(u.display_name,u.username) AS subtitle, COALESCE(v.summary,'') AS snippet, COALESCE(v.thumbnail_url,'') AS cover_url, '/videos/watch/' || v.video_id::text AS href, COALESCE(v.published_at,v.entry_created_at) AS created_at, 'title' AS match_field`).
			Joins(`JOIN "Users" u ON u.uuid = v.author_id AND u.deleted_at IS NULL AND u.is_active = true`).Where("v.status = 'published' AND v.visibility = 'public'")
	case "podcast:show":
		base = p.db.WithContext(ctx).Table("channels AS c").Select(`c.id::text AS id, c.name AS title, '' AS subtitle, COALESCE(c.description,'') AS snippet, COALESCE(c.cover_url,'') AS cover_url, '/podcasts/show/' || c.slug AS href, c.created_at, 'title' AS match_field`).
			Where("c.deleted_at IS NULL").Where("EXISTS (?)", contentmodule.PodcastQuery(p.db.WithContext(ctx)).Select("1").Where("posts.channel_id = c.id AND posts.status = 'published' AND posts.visibility = 'public'"))
	case "podcast:episode":
		base = p.db.WithContext(ctx).Table("(?) AS e", contentmodule.PodcastQuery(p.db.WithContext(ctx))).Select(`e.episode_id::text AS id, e.title, COALESCE(c.name,'') AS subtitle, COALESCE(e.shownotes,'') AS snippet, COALESCE(NULLIF(e.episode_cover_url,''),c.cover_url,'') AS cover_url, '/podcasts/episode/' || e.episode_id::text AS href, COALESCE(e.published_at,e.entry_created_at) AS created_at, 'title' AS match_field`).
			Joins("JOIN channels c ON c.id = e.channel_id AND c.deleted_at IS NULL").Where("e.status = 'published' AND e.visibility = 'public'")
	case "debate:debate":
		base = p.db.WithContext(ctx).Table("debates AS d").Select(`d.id::text AS id, d.title, COALESCE(u.display_name,u.username) AS subtitle, COALESCE(d.description,'') AS snippet, '' AS cover_url, '/debate/' || d.id::text AS href, d.created_at, 'title' AS match_field`).
			Joins(`JOIN "Users" u ON u.uuid = d.user_id AND u.deleted_at IS NULL AND u.is_active = true`).Where("d.deleted_at IS NULL AND d.status = 'active'")
	case "timeline:person":
		base = p.db.WithContext(ctx).Table("timeline_persons AS t").Select(`t.id::text AS id, t.name AS title, '' AS subtitle, COALESCE(t.bio,'') AS snippet, '' AS cover_url, '/timeline/person/' || t.id::text AS href, t.created_at, 'name' AS match_field`).Where("t.deleted_at IS NULL AND t.is_public = true")
	case "timeline:event":
		base = p.db.WithContext(ctx).Table("timeline_events AS t").Select(`t.id::text AS id, t.title, COALESCE(t.location,'') AS subtitle, COALESCE(t.description,'') AS snippet, '' AS cover_url, '/timeline?event=' || t.id::text AS href, t.event_date AS created_at, 'title' AS match_field`).Where("t.deleted_at IS NULL AND t.is_public = true")
	case "users:user":
		base = p.db.WithContext(ctx).Table(`"Users" AS u`).Select(`u.uuid::text AS id, COALESCE(NULLIF(u.display_name,''),u.username) AS title, '@' || u.username AS subtitle, COALESCE(u.bio,'') AS snippet, COALESCE(u.avatar_url,'') AS cover_url, '/users/' || u.username AS href, u.created_at, 'display_name' AS match_field`).
			Joins("LEFT JOIN user_settings s ON s.user_id = u.uuid").Where("u.deleted_at IS NULL AND u.is_active = true AND COALESCE(s.private_profile, false) = false")
	default:
		return search.ProviderSearchResult{}, apperr.BadRequest("search.invalid_type", fmt.Sprintf("Unknown %s search type", p.module))
	}
	if query != "" {
		pattern := "%" + query + "%"
		fields := map[string]string{
			"feed:source":     "(f.title ILIKE ? OR f.rss_url ILIKE ? OR f.site_url ILIKE ?)",
			"feed:article":    "(i.title ILIKE ? OR i.summary ILIKE ? OR i.author ILIKE ?)",
			"video:video":     "(v.title ILIKE ? OR v.summary ILIKE ?)",
			"podcast:show":    "(c.name ILIKE ? OR c.description ILIKE ?)",
			"podcast:episode": "(e.title ILIKE ? OR e.shownotes ILIKE ?)",
			"debate:debate":   "(d.title ILIKE ? OR d.description ILIKE ? OR d.content ILIKE ?)",
			"timeline:person": "(t.name ILIKE ? OR t.bio ILIKE ?)",
			"timeline:event":  "(t.title ILIKE ? OR t.description ILIKE ? OR t.content ILIKE ? OR t.location ILIKE ?)",
			"users:user":      "(u.username ILIKE ? OR u.display_name ILIKE ? OR u.bio ILIKE ?)",
		}[p.module+":"+req.Type]
		n := strings.Count(fields, "?")
		args := make([]any, n)
		for i := range args {
			args[i] = pattern
		}
		base = base.Where(fields, args...)
	}
	var total int64
	if req.ExactTotal {
		if err := base.Count(&total).Error; err != nil {
			return search.ProviderSearchResult{}, err
		}
	}
	order := "created_at DESC, id DESC"
	if req.Sort == "relevance" && query != "" {
		order = "CASE WHEN title ILIKE ? THEN 0 ELSE 1 END, created_at DESC, id DESC"
		base = base.Order(gorm.Expr(order, query+"%"))
	} else {
		base = base.Order(order)
	}
	if err := base.Limit(req.Limit).Offset(req.Offset).Scan(&rows).Error; err != nil {
		return search.ProviderSearchResult{}, err
	}
	out := search.ProviderSearchResult{Items: make([]search.SearchHit, 0, len(rows))}
	if req.ExactTotal {
		out.Total = &total
	}
	for _, row := range rows {
		out.Items = append(out.Items, search.SearchHit{Module: p.module, Type: req.Type, ID: row.ID, Title: row.Title, Subtitle: row.Subtitle, Snippet: searchSnippet(row.Snippet), CoverURL: row.CoverURL, Href: row.Href, MatchedFields: []string{row.MatchField}})
	}
	return out, nil
}
