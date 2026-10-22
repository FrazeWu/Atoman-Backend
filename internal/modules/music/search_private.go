package music

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

type PrivatePlaylistSearchResult struct {
	Items []model.Playlist
	Total *int64
}

// 管理员也只搜索自己的集合；收藏不能绕过歌单撤回和软删除。
func (s *Service) SearchPrivatePlaylists(ctx context.Context, user authctx.CurrentUser, bookmarked bool, opts MusicSearchOptions, sort string) (PrivatePlaylistSearchResult, error) {
	result := PrivatePlaylistSearchResult{Items: []model.Playlist{}}
	if user.ID == uuid.Nil {
		return result, apperr.Unauthorized("Login required")
	}
	if sort != "" && sort != "recent" && sort != "relevance" {
		return result, apperr.BadRequest("search.invalid_sort", "Unsupported private playlist sort")
	}
	if opts.Limit < 1 {
		opts.Limit = 24
	}
	opts.Limit, opts.Offset = min(opts.Limit, 100), max(opts.Offset, 0)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&model.Playlist{})
		if bookmarked {
			query = query.Where(`EXISTS (SELECT 1 FROM music_playlist_bookmarks b WHERE b.playlist_id = music_playlists.id AND b.user_id = ? AND b.deleted_at IS NULL)
AND (is_public = true OR music_playlists.user_id = ?)`, user.ID, user.ID)
		} else {
			query = query.Where("user_id = ?", user.ID)
		}
		keyword := strings.TrimSpace(opts.Query)
		pattern := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(keyword)
		if keyword != "" {
			query = query.Where("name ILIKE ? ESCAPE '\\'", "%"+pattern+"%")
		}
		if opts.ExactTotal {
			var total int64
			if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
				return err
			}
			result.Total = &total
		}
		if sort != "recent" && keyword != "" {
			query = query.Clauses(clause.OrderBy{Expression: clause.Expr{SQL: "CASE WHEN LOWER(name) = LOWER(?) THEN 0 WHEN name ILIKE ? ESCAPE '\\' THEN 1 ELSE 2 END, updated_at DESC, id ASC", Vars: []any{keyword, pattern + "%"}}})
		} else {
			query = query.Order("updated_at DESC, id ASC")
		}
		if err := query.Select("id, user_id, name, LEFT(description, 240) AS description, cover_url").Limit(opts.Limit).Offset(opts.Offset).Find(&result.Items).Error; err != nil {
			return err
		}
		for i := range result.Items {
			result.Items[i].CoverURL = resolveMusicMediaURL(result.Items[i].CoverURL)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
