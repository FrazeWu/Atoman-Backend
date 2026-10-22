package shortnote

import (
	"context"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PublicSearchOptions struct {
	Query, Sort   string
	AuthorIDs     []uuid.UUID
	Limit, Offset int
	ExactTotal    bool
}

// 短笺没有草稿或私密状态，与匿名 List/Get 一样仅排除软删除记录。
func (s *Service) SearchPublic(ctx context.Context, opts PublicSearchOptions) ([]model.ShortNote, int64, error) {
	text := strings.ToLower(strings.TrimSpace(opts.Query))
	escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(text)
	query := s.db.WithContext(ctx).Model(&model.ShortNote{}).
		Where("LOWER(short_notes.content) LIKE ? ESCAPE '\\'", "%"+escaped+"%")
	if len(opts.AuthorIDs) > 0 {
		query = query.Where("short_notes.user_id IN ?", opts.AuthorIDs)
	}
	var total int64
	if opts.ExactTotal {
		if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}
	if opts.Sort != "recent" {
		query = query.Order(clause.OrderBy{Expression: clause.Expr{SQL: "CASE WHEN LOWER(short_notes.content) = ? THEN 2 WHEN LOWER(short_notes.content) LIKE ? ESCAPE '\\' THEN 1 ELSE 0 END DESC, short_notes.created_at DESC, short_notes.id DESC", Vars: []any{text, escaped + "%"}}})
	} else {
		query = query.Order("short_notes.created_at DESC, short_notes.id DESC")
	}
	var items []model.ShortNote
	err := query.Preload("User").Offset(opts.Offset).Limit(opts.Limit).Find(&items).Error
	return items, total, err
}
