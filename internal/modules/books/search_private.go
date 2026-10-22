package books

import (
	"context"
	"database/sql"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 私人查询只返回搜索所需的投影，不序列化草稿快照、标签或整篇笔记。
type PrivateBookSearchItem struct {
	ID      uuid.UUID
	WorkID  uuid.UUID
	Title   string
	Snippet string
}

type PrivateBookSearchResult struct {
	Items []PrivateBookSearchItem
	Total *int64
}

func (s *Service) SearchPrivateBooks(ctx context.Context, user authctx.CurrentUser, kind string, opts BookSearchOptions) (PrivateBookSearchResult, error) {
	result := PrivateBookSearchResult{Items: []PrivateBookSearchItem{}}
	if user.ID == uuid.Nil {
		return result, apperr.Unauthorized("Login required")
	}
	if opts.Sort != "" && opts.Sort != "recent" && opts.Sort != "relevance" {
		return result, apperr.BadRequest("search.invalid_sort", "Unsupported private book sort")
	}
	opts.Limit, opts.Offset = normalizeCatalogPagination(opts.Limit, opts.Offset)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var query *gorm.DB
		var title, body, id, updated, workID string
		switch kind {
		case "note":
			query = tx.Model(&model.BookPrivateNote{}).Where("user_id = ? AND purged_at IS NULL", user.ID)
			title, body, id, updated, workID = "title", "body", "id", "updated_at", "work_id"
		case "book_list", "bookmark":
			query = tx.Model(&model.BookList{})
			if kind == "book_list" {
				query = query.Where("book_lists.user_id = ?", user.ID)
				title, body = "draft_json->>'title'", "draft_json->>'description'"
			} else {
				// 收藏关系不授予访问权限；撤回或他人的私有书单不参与计数和摘要。
				query = query.Where(`EXISTS (SELECT 1 FROM book_list_bookmarks b WHERE b.list_id = book_lists.id AND b.user_id = ?)
AND published_at IS NOT NULL AND (visibility = 'public' OR book_lists.user_id = ?)`, user.ID, user.ID)
				title, body = "published_json->>'title'", "published_json->>'description'"
			}
			id, updated, workID = "book_lists.id", "book_lists.updated_at", "NULL::uuid"
			if kind == "bookmark" {
				// 其他作者修改草稿不能改变收藏搜索的排序、泄漏私人编辑时间。
				updated = "book_lists.published_at"
			}
		case "library":
			query = tx.Model(&model.BookLibraryEntry{}).Where("book_library_entries.user_id = ?", user.ID).
				Joins("JOIN book_works w ON w.id = book_library_entries.work_id AND w.deleted_at IS NULL").
				Where("w.id IN (?)", publicSearchWorks(tx.Session(&gorm.Session{NewDB: true})).Select("book_works.id"))
			title, body, id, updated, workID = "w.title", "w.description", "book_library_entries.id", "book_library_entries.updated_at", "w.id"
		default:
			return apperr.BadRequest("search.invalid_type", "Unknown private book type")
		}
		keyword := strings.TrimSpace(opts.Query)
		if keyword != "" {
			pattern := "%" + escapeBookPrivateSearch(keyword) + "%"
			query = query.Where("("+title+" ILIKE ? ESCAPE '\\' OR "+body+" ILIKE ? ESCAPE '\\')", pattern, pattern)
		}
		if opts.ExactTotal {
			var total int64
			if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
				return err
			}
			result.Total = &total
		}
		if opts.Sort != "recent" && keyword != "" {
			query = query.Clauses(clause.OrderBy{Expression: clause.Expr{SQL: "CASE WHEN LOWER(" + title + ") = LOWER(?) THEN 0 WHEN " + title + " ILIKE ? ESCAPE '\\' THEN 1 ELSE 2 END, " + updated + " DESC, " + id + " ASC", Vars: []any{keyword, escapeBookPrivateSearch(keyword) + "%"}}})
		} else {
			query = query.Order(updated + " DESC, " + id + " ASC")
		}
		return query.Select(id + " AS id, " + workID + " AS work_id, COALESCE(NULLIF(" + title + ", ''), '未命名') AS title, LEFT(COALESCE(" + body + ", ''), 240) AS snippet").Limit(opts.Limit).Offset(opts.Offset).Scan(&result.Items).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
