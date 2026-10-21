package books

import (
	"context"
	"database/sql"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 领域查询不依赖统一搜索协议；旧接口保持原来的行为。
type BookSearchOptions struct {
	Query      string
	Sort       string
	Limit      int
	Offset     int
	ExactTotal bool
}

type WorkSearchOptions struct {
	BookSearchOptions
	Languages []string
	AuthorIDs []uuid.UUID
	YearFrom  int
	YearTo    int
}

type BookWorkSearchItem struct {
	ID                uuid.UUID
	Title             string
	Subtitle          string
	OriginalTitle     string
	Description       string
	Language          string
	CoverURL          string
	Authors           []BookPublicPersonDTO `gorm:"-"`
	BookWorkRatingDTO `gorm:"embedded"`
}

type WorkSearchResult struct {
	Items []BookWorkSearchItem
	Total *int64
}

// EXISTS 避免多作者、多版本连接放大结果；作品必须有当前 Wiki 修订。
func publicSearchWorks(db *gorm.DB) *gorm.DB {
	return db.Model(&model.BookWork{}).Where("book_works.lifecycle_status = ? AND book_works.edit_status <> ?", model.BookLifecycleStatusActive, model.BookEditStatusClosed).
		Where("EXISTS (SELECT 1 FROM revisions WHERE content_type = 'book_work' AND content_id = book_works.id AND is_current = true AND deleted_at IS NULL)")
}

func applyWorkSearchFilters(db *gorm.DB, opts WorkSearchOptions) *gorm.DB {
	if len(opts.Languages) > 0 {
		db = db.Where("book_works.language IN ?", opts.Languages)
	}
	if len(opts.AuthorIDs) > 0 {
		db = db.Where(`EXISTS (SELECT 1 FROM book_contributions bc JOIN book_people bp ON bp.id = bc.person_id
 WHERE bc.work_id = book_works.id AND bc.edition_id IS NULL AND bc.role = 'author'
 AND bc.deleted_at IS NULL AND bp.deleted_at IS NULL AND bp.lifecycle_status = 'active' AND bp.id IN ?)`, opts.AuthorIDs)
	}
	if opts.YearFrom > 0 || opts.YearTo > 0 {
		editions := db.Session(&gorm.Session{NewDB: true}).Model(&model.BookEdition{}).
			Select("1").Where("book_editions.work_id = book_works.id AND book_editions.lifecycle_status = 'active' AND book_editions.edit_status <> 'closed'")
		if opts.YearFrom > 0 {
			editions = editions.Where("published_date >= make_date(?, 1, 1)", opts.YearFrom)
		}
		if opts.YearTo > 0 {
			editions = editions.Where("published_date < make_date(?, 1, 1) + INTERVAL '1 year'", opts.YearTo)
		}
		db = db.Where("EXISTS (?)", editions)
	}
	return db
}

func buildWorkSearchQuery(db *gorm.DB, opts WorkSearchOptions) *gorm.DB {
	base := applyWorkSearchFilters(publicSearchWorks(db), opts)
	if opts.Query != "" {
		pattern := "%" + escapeBookCatalogQuery(opts.Query) + "%"
		base = base.Where(`(book_works.title ILIKE ? ESCAPE '\' OR book_works.original_title ILIKE ? ESCAPE '\'
 OR book_works.subtitle ILIKE ? ESCAPE '\' OR EXISTS (
 SELECT 1 FROM book_contributions bc JOIN book_people bp ON bp.id = bc.person_id
 WHERE bc.work_id = book_works.id AND bc.edition_id IS NULL AND bc.deleted_at IS NULL
 AND bp.deleted_at IS NULL AND bp.lifecycle_status = 'active' AND bp.name ILIKE ? ESCAPE '\'))`, pattern, pattern, pattern, pattern)
	}
	return base
}

func applyWorkSearchRanking(db *gorm.DB, opts WorkSearchOptions) *gorm.DB {
	switch opts.Sort {
	case "rating_desc":
		return db.Order("ratings.weighted_rating_score DESC, ratings.rating_count DESC, book_works.id ASC")
	case "newest":
		return db.Order("book_works.created_at DESC, book_works.id ASC")
	case "title":
		return db.Order("book_works.title ASC, book_works.id ASC")
	default:
		if opts.Query == "" {
			return db.Order("book_works.title ASC, book_works.id ASC")
		}
		// 精确标题 > 标题前缀 > 原名/副标题 > 标题包含 > 作者命中。
		return db.Clauses(clause.OrderBy{Expression: clause.Expr{SQL: `CASE
 WHEN LOWER(book_works.title) = LOWER(?) THEN 0
 WHEN book_works.title ILIKE ? ESCAPE '\' THEN 1
 WHEN book_works.original_title ILIKE ? ESCAPE '\' OR book_works.subtitle ILIKE ? ESCAPE '\' THEN 2
 WHEN book_works.title ILIKE ? ESCAPE '\' THEN 3 ELSE 4 END, book_works.title ASC, book_works.id ASC`,
			Vars: []any{opts.Query, escapeBookCatalogQuery(opts.Query) + "%", "%" + escapeBookCatalogQuery(opts.Query) + "%", "%" + escapeBookCatalogQuery(opts.Query) + "%", "%" + escapeBookCatalogQuery(opts.Query) + "%"}}})
	}
}

func (s *Service) SearchWorksAdvanced(ctx context.Context, opts WorkSearchOptions) (WorkSearchResult, error) {
	opts.Query = strings.TrimSpace(opts.Query)
	opts.Limit, opts.Offset = normalizeCatalogPagination(opts.Limit, opts.Offset)
	result := WorkSearchResult{Items: []BookWorkSearchItem{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		base := buildWorkSearchQuery(tx, opts)
		// 只有评分排序需要全体候选的聚合；普通搜索仅聚合本页作品。
		if opts.Sort == "rating_desc" {
			base = base.Joins("JOIN (?) AS ratings ON ratings.work_id = book_works.id", bookWeightedRatingQuery(tx)).
				Where("ratings.rating_count >= ? AND ratings.weighted_rating_score IS NOT NULL", bookMinimumRatingCount)
		}
		if opts.ExactTotal {
			var total int64
			if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
				return err
			}
			result.Total = &total
		}
		query := applyWorkSearchRanking(base, opts).Select(`book_works.id, book_works.id AS work_id, book_works.title,
 book_works.subtitle, book_works.original_title, book_works.description, book_works.language,
 COALESCE((SELECT cover_url FROM book_editions e WHERE e.work_id = book_works.id
 AND e.deleted_at IS NULL AND e.lifecycle_status = 'active' AND e.edit_status <> 'closed' AND e.cover_url <> ''
 ORDER BY e.published_date ASC NULLS LAST, e.id ASC LIMIT 1), '') AS cover_url`)
		if err := query.Limit(opts.Limit).Offset(opts.Offset).Scan(&result.Items).Error; err != nil {
			return err
		}
		if len(result.Items) == 0 {
			return nil
		}
		ids := make([]uuid.UUID, 0, len(result.Items))
		for _, item := range result.Items {
			ids = append(ids, item.ID)
		}
		var authors []struct {
			WorkID uuid.UUID
			ID     uuid.UUID
			Name   string
		}
		if err := tx.Table("book_contributions bc").Select("bc.work_id, bp.id, bp.name").
			Joins("JOIN book_people bp ON bp.id = bc.person_id AND bp.deleted_at IS NULL AND bp.lifecycle_status = 'active'").
			Where("bc.work_id IN ? AND bc.edition_id IS NULL AND bc.role = 'author' AND bc.deleted_at IS NULL", ids).
			Order("bc.position ASC, bp.id ASC").Scan(&authors).Error; err != nil {
			return err
		}
		var ratings []BookWorkRatingDTO
		if err := tx.Table("(?) AS ratings", bookWeightedRatingQuery(tx).Where("votes.work_id IN ?", ids)).Scan(&ratings).Error; err != nil {
			return err
		}
		for i := range result.Items {
			item := &result.Items[i]
			item.Authors = []BookPublicPersonDTO{}
			for _, author := range authors {
				if author.WorkID == item.ID {
					item.Authors = append(item.Authors, BookPublicPersonDTO{ID: author.ID.String(), Name: author.Name, Role: "author"})
				}
			}
			for _, rating := range ratings {
				if rating.WorkID == item.ID {
					item.BookWorkRatingDTO = rating
				}
			}
			finishBookWorkRating(&item.BookWorkRatingDTO)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
