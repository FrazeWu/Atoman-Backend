package music

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MusicSearchVisibility string

const (
	MusicSearchPublic MusicSearchVisibility = "public"
	// 仅用于兼容旧 API：作者可见自己的草稿和私人歌单，管理员可见非公开条目。
	MusicSearchLegacy MusicSearchVisibility = "legacy"
)

type MusicSearchOptions struct {
	Query      string
	Type       string // 空值分别查询四种类型，每种独立分页。
	Limit      int
	Offset     int
	ExactTotal bool
	Summary    bool
	Visibility MusicSearchVisibility
}

type MusicSearchResult struct {
	Songs     []model.Song
	Albums    []model.Album
	Artists   []model.Artist
	Playlists []model.Playlist
	Totals    map[string]int64 // 仅 ExactTotal=true 时填充。
}

// SearchCatalog 是新旧 API 唯一的领域查询入口。默认公开模式，绝不隐式继承管理员权限。
func (s *Service) SearchCatalog(ctx context.Context, viewer *authctx.CurrentUser, opts MusicSearchOptions) (MusicSearchResult, error) {
	result := MusicSearchResult{Songs: []model.Song{}, Albums: []model.Album{}, Artists: []model.Artist{}, Playlists: []model.Playlist{}}
	if opts.ExactTotal {
		result.Totals = map[string]int64{"song": 0, "album": 0, "artist": 0, "playlist": 0}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	switch opts.Visibility {
	case "", MusicSearchPublic:
		viewer = nil
	case MusicSearchLegacy:
	default:
		return result, apperr.BadRequest("music.invalid_search_visibility", "Invalid search visibility")
	}
	if opts.Type != "" && opts.Type != "song" && opts.Type != "album" && opts.Type != "artist" && opts.Type != "playlist" {
		return result, apperr.BadRequest("music.invalid_search_type", "Invalid search type")
	}
	opts.Query = strings.TrimSpace(opts.Query)
	if opts.Query == "" {
		return result, nil // 保留旧搜索的空关键词语义，不把内容列表伪装成搜索。
	}
	if opts.Limit < 1 {
		opts.Limit = 20
	}
	opts.Limit = min(opts.Limit, 100) // 旧 API 上限 100；统一协议另行限制为 50。
	opts.Offset = max(opts.Offset, 0)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, kind := range []string{"song", "album", "artist", "playlist"} {
			if opts.Type != "" && opts.Type != kind {
				continue
			}
			query, rank := musicSearchQuery(tx, viewer, kind, opts.Query)
			if opts.ExactTotal {
				var total int64
				if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
					return err
				}
				result.Totals[kind] = total
			}
			query = query.Clauses(clause.OrderBy{Expression: rank}).Limit(opts.Limit).Offset(opts.Offset)
			switch kind {
			case "song":
				if opts.Summary {
					query = query.Select(songSummarySelection)
				}
				if err := query.Preload("Album", visibleAlbumPreload(viewer)).Preload("Artists", visibleArtistPreload(viewer)).Find(&result.Songs).Error; err != nil {
					return err
				}
				for i := range result.Songs {
					resolveSongEffectiveSources(&result.Songs[i])
					result.Songs[i].AudioURL = resolveMusicMediaURL(result.Songs[i].AudioURL)
					result.Songs[i].CoverURL = resolveMusicMediaURL(result.Songs[i].CoverURL)
				}
			case "album":
				if err := query.Preload("Artists", visibleArtistPreload(viewer)).Find(&result.Albums).Error; err != nil {
					return err
				}
				for i := range result.Albums {
					resolveAlbumMediaURLs(&result.Albums[i])
				}
			case "artist":
				if err := query.Find(&result.Artists).Error; err != nil {
					return err
				}
				for i := range result.Artists {
					result.Artists[i].ImageURL = resolveMusicMediaURL(result.Artists[i].ImageURL)
				}
			case "playlist":
				if err := query.Find(&result.Playlists).Error; err != nil {
					return err
				}
				if err := NewRepo(tx).HydratePlaylistStats(result.Playlists); err != nil {
					return err
				}
				for i := range result.Playlists {
					result.Playlists[i].CoverURL = resolveMusicMediaURL(result.Playlists[i].CoverURL)
				}
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}

// 所有关系匹配均为 EXISTS；一个实体只有一行，相关度不会因别名/贡献角色重复而改变页大小。
func musicSearchQuery(db *gorm.DB, viewer *authctx.CurrentUser, kind, query string) (*gorm.DB, clause.Expr) {
	literal := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query)
	pattern, prefix := "%"+literal+"%", literal+"%"
	switch kind {
	case "song":
		artistMatch, artistArgs := musicRelatedArtistMatch(viewer, "song_artists", "song_id", `"Songs".id`, pattern)
		albumVisibility, albumArgs := musicEntryVisibilityCondition("search_album", "uploaded_by", viewer, true)
		albumMatch := `EXISTS (SELECT 1 FROM "Albums" search_album WHERE search_album.id = "Songs".album_id AND ` + albumVisibility + ` AND LOWER(search_album.title) LIKE LOWER(?))`
		albumArgs = append(albumArgs, pattern)
		lyricsMatch := `EXISTS (SELECT 1 FROM music_song_lyrics lyrics WHERE lyrics.song_id = "Songs".id AND lyrics.deleted_at IS NULL AND (LOWER(lyrics.content) LIKE LOWER(?) OR LOWER(lyrics.translation) LIKE LOWER(?)))`
		whereArgs := []any{pattern}
		whereArgs = append(whereArgs, albumArgs...)
		whereArgs = append(whereArgs, artistArgs...)
		whereArgs = append(whereArgs, pattern, pattern)
		base := scopeVisibleMusicEntries(db.Model(&model.Song{}), `"Songs"`, "uploaded_by", viewer, false).
			Where(`(LOWER("Songs".title) LIKE LOWER(?) OR `+albumMatch+` OR `+artistMatch+` OR `+lyricsMatch+`)`, whereArgs...)
		prefixArtist, prefixArgs := musicRelatedArtistMatch(viewer, "song_artists", "song_id", `"Songs".id`, prefix)
		rankArgs := []any{query, prefix}
		rankArgs = append(rankArgs, albumArgs[:len(albumArgs)-1]...)
		rankArgs = append(rankArgs, prefix)
		rankArgs = append(rankArgs, prefixArgs...)
		return base, clause.Expr{SQL: `CASE WHEN LOWER("Songs".title) = LOWER(?) THEN 0 WHEN LOWER("Songs".title) LIKE LOWER(?) THEN 1 WHEN ` + albumMatch + ` OR ` + prefixArtist + ` THEN 2 ELSE 3 END, "Songs".play_count DESC, "Songs".title ASC, "Songs".id ASC`, Vars: rankArgs}
	case "album":
		artistMatch, args := musicRelatedArtistMatch(viewer, "album_artists", "album_id", `"Albums".id`, pattern)
		base := scopeVisibleMusicEntries(db.Model(&model.Album{}), `"Albums"`, "uploaded_by", viewer, false).
			Where(`(LOWER("Albums".title) LIKE LOWER(?) OR `+artistMatch+`)`, append([]any{pattern}, args...)...)
		return base, clause.Expr{SQL: `CASE WHEN LOWER("Albums".title) = LOWER(?) THEN 0 WHEN LOWER("Albums".title) LIKE LOWER(?) THEN 1 ELSE 2 END, "Albums".hot_score DESC, "Albums".title ASC, "Albums".id ASC`, Vars: []any{query, prefix}}
	case "artist":
		aliasMatch := `EXISTS (SELECT 1 FROM artist_aliases aliases WHERE aliases.artist_id = "Artists".id AND aliases.deleted_at IS NULL AND LOWER(aliases.alias) LIKE LOWER(?))`
		base := scopeVisibleMusicEntries(db.Model(&model.Artist{}), `"Artists"`, "created_by", viewer, false).
			Where(`(LOWER("Artists".name) LIKE LOWER(?) OR LOWER(`+artistDisambiguationSearchExpression+`) LIKE LOWER(?) OR LOWER("Artists".legal_name) LIKE LOWER(?) OR `+aliasMatch+`)`, pattern, pattern, pattern, pattern)
		return base, clause.Expr{SQL: `CASE WHEN LOWER("Artists".name) = LOWER(?) THEN 0 WHEN LOWER("Artists".name) LIKE LOWER(?) OR ` + aliasMatch + ` THEN 1 ELSE 2 END, "Artists".name ASC, "Artists".id ASC`, Vars: []any{query, prefix, prefix}}
	default: // SearchCatalog 已校验为 playlist。
		base := db.Model(&model.Playlist{}).Where("LOWER(name) LIKE LOWER(?)", pattern)
		if viewer != nil {
			base = base.Where("(is_public = true OR user_id = ?)", viewer.ID)
		} else {
			base = base.Where("is_public = true")
		}
		return base, clause.Expr{SQL: `CASE WHEN LOWER(name) = LOWER(?) THEN 0 WHEN LOWER(name) LIKE LOWER(?) THEN 1 ELSE 2 END, updated_at DESC, id ASC`, Vars: []any{query, prefix}}
	}
}

func musicRelatedArtistMatch(viewer *authctx.CurrentUser, relation, column, target, pattern string) (string, []any) {
	visibility, args := musicEntryVisibilityCondition("search_artist", "created_by", viewer, true)
	// 表/列名来自上方两个固定调用点，不接受请求中的标识符。
	sql := fmt.Sprintf(`EXISTS (SELECT 1 FROM %s credits JOIN "Artists" search_artist ON search_artist.id = credits.artist_id
 WHERE credits.%s = %s AND %s AND (LOWER(search_artist.name) LIKE LOWER(?) OR EXISTS (
 SELECT 1 FROM artist_aliases aliases WHERE aliases.artist_id = search_artist.id AND aliases.deleted_at IS NULL AND LOWER(aliases.alias) LIKE LOWER(?))))`, relation, column, target, visibility)
	return sql, append(args, pattern, pattern)
}
