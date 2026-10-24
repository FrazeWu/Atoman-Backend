package blog

import (
	"context"
	"strings"

	"atoman/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ContainerSearchOptions struct {
	Query, Sort   string
	Limit, Offset int
	ExactTotal    bool
}

// 频道、合集没有单独的公开状态：只有包含公开已发布博客文章才具备全站搜索资格。
const publicBlogEntry = `p.kind = 'blog' AND p.status = 'published'
	AND p.visibility IN ('', 'public') AND p.deleted_at IS NULL
	AND EXISTS (SELECT 1 FROM content_blog_extensions b WHERE b.content_id = p.id)`

func (s *Service) SearchPublicChannels(ctx context.Context, opts ContainerSearchOptions) ([]model.Channel, int64, error) {
	query := s.db.WithContext(ctx).Model(&model.Channel{}).
		Where(`EXISTS (SELECT 1 FROM content_entries p WHERE p.channel_id = channels.id AND ` + publicBlogEntry + `)`)
	query = filterBlogContainer(query, "channels", opts.Query)
	var total int64
	if opts.ExactTotal {
		if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}
	var items []model.Channel
	err := orderBlogContainer(query, "channels", opts).Offset(opts.Offset).Limit(opts.Limit).Find(&items).Error
	return items, total, err
}

func (s *Service) SearchPublicCollections(ctx context.Context, opts ContainerSearchOptions) ([]model.ContentCollection, int64, error) {
	query := s.db.WithContext(ctx).Model(&model.ContentCollection{}).
		Where(`EXISTS (SELECT 1 FROM channels c WHERE c.id = content_collections.channel_id AND c.deleted_at IS NULL)`).
		Where(`EXISTS (SELECT 1 FROM content_collection_memberships cm
			JOIN content_entries p ON p.id = cm.content_id
			WHERE cm.collection_id = content_collections.id AND p.channel_id = content_collections.channel_id AND ` + publicBlogEntry + `)`)
	query = filterBlogContainer(query, "content_collections", opts.Query)
	var total int64
	if opts.ExactTotal {
		if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}
	var items []model.ContentCollection
	err := orderBlogContainer(query, "content_collections", opts).Offset(opts.Offset).Limit(opts.Limit).Find(&items).Error
	return items, total, err
}

// table 只来自上面两个固定调用点；用户输入始终作为绑定参数。
func filterBlogContainer(db *gorm.DB, table, query string) *gorm.DB {
	pattern := "%" + escapeBlogSearchLike(strings.ToLower(strings.TrimSpace(query))) + "%"
	return db.Where("(LOWER("+table+".name) LIKE ? ESCAPE '\\' OR LOWER("+table+".description) LIKE ? ESCAPE '\\')", pattern, pattern)
}

func orderBlogContainer(db *gorm.DB, table string, opts ContainerSearchOptions) *gorm.DB {
	if opts.Sort != blogSearchSortRecent {
		text := strings.ToLower(strings.TrimSpace(opts.Query))
		db = db.Clauses(clause.OrderBy{Expression: clause.Expr{SQL: "CASE WHEN LOWER(" + table + ".name) = ? THEN 2 WHEN LOWER(" + table + ".name) LIKE ? ESCAPE '\\' THEN 1 ELSE 0 END DESC, " + table + ".created_at DESC, " + table + ".id DESC", Vars: []any{text, escapeBlogSearchLike(text) + "%"}}})
		return db
	}
	return db.Order(table + ".created_at DESC, " + table + ".id DESC")
}
