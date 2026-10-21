package books

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BookAuthorSearchItem struct {
	ID          uuid.UUID
	Name        string
	Description string
	WorkCount   int64
}

type AuthorSearchResult struct {
	Items []BookAuthorSearchItem
	Total *int64
}

func (s *Service) SearchAuthorsAdvanced(ctx context.Context, opts BookSearchOptions) (AuthorSearchResult, error) {
	opts.Query = strings.TrimSpace(opts.Query)
	opts.Limit, opts.Offset = normalizeCatalogPagination(opts.Limit, opts.Offset)
	result := AuthorSearchResult{Items: []BookAuthorSearchItem{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		base := tx.Model(&model.BookPerson{}).Where("book_people.lifecycle_status = 'active'")
		if opts.Query != "" {
			base = base.Where("book_people.name ILIKE ? ESCAPE '\\'", "%"+escapeBookCatalogQuery(opts.Query)+"%")
		}
		if opts.ExactTotal {
			var total int64
			if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
				return err
			}
			result.Total = &total
		}
		if opts.Sort == "newest" {
			base = base.Order("book_people.created_at DESC, book_people.id ASC")
		} else if opts.Sort == "relevance" && opts.Query != "" {
			base = base.Clauses(clause.OrderBy{Expression: clause.Expr{SQL: `CASE
 WHEN LOWER(book_people.name) = LOWER(?) THEN 0 WHEN book_people.name ILIKE ? ESCAPE '\' THEN 1 ELSE 2 END,
 book_people.name ASC, book_people.id ASC`, Vars: []any{opts.Query, escapeBookCatalogQuery(opts.Query) + "%"}}})
		} else {
			base = base.Order("book_people.name ASC, book_people.id ASC")
		}
		// 只统计仍公开可用的 Wiki 作品，不包含草稿、合并源或仅版本级贡献。
		works := publicSearchWorks(tx.Session(&gorm.Session{NewDB: true})).Select("book_works.id").
			Where("EXISTS (SELECT 1 FROM book_contributions bc WHERE bc.work_id = book_works.id AND bc.person_id = book_people.id AND bc.edition_id IS NULL AND bc.role = 'author' AND bc.deleted_at IS NULL)")
		return base.Select("book_people.id, book_people.name, book_people.description, (?) AS work_count", tx.Session(&gorm.Session{NewDB: true}).Table("(?) AS works", works).Select("COUNT(*)")).
			Limit(opts.Limit).Offset(opts.Offset).Scan(&result.Items).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}

// 只投影已发布快照，绝不读取 DraftJSON、ChangesJSON 或私人更新时间。
type PublicBookListSearchItem struct {
	ID          uuid.UUID
	OwnerID     uuid.UUID
	OwnerName   string
	Title       string
	Description string
	ItemCount   int
	PublishedAt *time.Time
}

type PublicBookListSearchResult struct {
	Items []PublicBookListSearchItem
	Total *int64
}

func (s *Service) SearchPublicBookListsAdvanced(ctx context.Context, opts BookSearchOptions) (PublicBookListSearchResult, error) {
	opts.Query = strings.TrimSpace(opts.Query)
	opts.Limit, opts.Offset = normalizeCatalogPagination(opts.Limit, opts.Offset)
	result := PublicBookListSearchResult{Items: []PublicBookListSearchItem{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		base := tx.Model(&model.BookList{}).Where("book_lists.visibility = 'public' AND book_lists.published_at IS NOT NULL")
		if opts.Query != "" {
			pattern := "%" + escapeBookCatalogQuery(opts.Query) + "%"
			base = base.Where(`(book_lists.published_json->>'title' ILIKE ? ESCAPE '\' OR book_lists.published_json->>'description' ILIKE ? ESCAPE '\')`, pattern, pattern)
		}
		if opts.ExactTotal {
			var total int64
			if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
				return err
			}
			result.Total = &total
		}
		if opts.Sort == "title" {
			base = base.Order("book_lists.published_json->>'title' ASC, book_lists.id ASC")
		} else if opts.Sort == "relevance" && opts.Query != "" {
			base = base.Clauses(clause.OrderBy{Expression: clause.Expr{SQL: `CASE
 WHEN LOWER(book_lists.published_json->>'title') = LOWER(?) THEN 0
 WHEN book_lists.published_json->>'title' ILIKE ? ESCAPE '\' THEN 1
 WHEN book_lists.published_json->>'title' ILIKE ? ESCAPE '\' THEN 2 ELSE 3 END,
 book_lists.published_at DESC, book_lists.id ASC`, Vars: []any{opts.Query, escapeBookCatalogQuery(opts.Query) + "%", "%" + escapeBookCatalogQuery(opts.Query) + "%"}}})
		} else {
			base = base.Order("book_lists.published_at DESC, book_lists.id ASC")
		}
		return base.Joins(`LEFT JOIN "Users" u ON u.uuid = book_lists.user_id AND u.deleted_at IS NULL`).
			Select(`book_lists.id, book_lists.user_id AS owner_id, COALESCE(u.username, '') AS owner_name,
 book_lists.published_json->>'title' AS title, book_lists.published_json->>'description' AS description,
 jsonb_array_length(COALESCE(book_lists.published_json->'items', '[]'::jsonb)) AS item_count, book_lists.published_at`).
			Limit(opts.Limit).Offset(opts.Offset).Scan(&result.Items).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
