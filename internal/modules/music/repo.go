package music

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

type BookmarkSort string

const (
	BookmarkSortLatest  BookmarkSort = "latest"
	BookmarkSortPopular BookmarkSort = "popular"
	BookmarkSortName    BookmarkSort = "name"
)

func normalizeBookmarkSort(sort string) BookmarkSort {
	if strings.EqualFold(strings.TrimSpace(sort), string(BookmarkSortName)) {
		return BookmarkSortName
	}
	if strings.EqualFold(strings.TrimSpace(sort), string(BookmarkSortPopular)) {
		return BookmarkSortPopular
	}
	return BookmarkSortLatest
}

func (r *Repo) CreateEdit(edit *model.MusicEdit) error { return r.db.Create(edit).Error }

func (r *Repo) GetEdit(id uuid.UUID) (model.MusicEdit, error) {
	var edit model.MusicEdit
	err := r.db.First(&edit, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.MusicEdit{}, err
	}
	return edit, err
}

func (r *Repo) SaveEdit(edit *model.MusicEdit) error { return r.db.Save(edit).Error }

func (r *Repo) ClaimOpenEdit(id uuid.UUID, status string) (bool, error) {
	result := r.db.Model(&model.MusicEdit{}).
		Where("id = ? AND status = ?", id, "open").
		Update("status", status)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

type ListEditsQuery struct {
	Status      string
	EntityType  string
	Type        string
	SubmittedBy *uuid.UUID
	Page        int
	PageSize    int
}

func (r *Repo) ListEdits(query ListEditsQuery) ([]model.MusicEdit, int64, error) {
	db := r.db.Model(&model.MusicEdit{})
	if status := strings.TrimSpace(query.Status); status != "" {
		db = db.Where("status = ?", status)
	}
	if entityType := strings.TrimSpace(query.EntityType); entityType != "" {
		db = db.Where("entity_type = ?", entityType)
	}
	if editType := strings.TrimSpace(query.Type); editType != "" {
		db = db.Where("type = ?", editType)
	}
	if query.SubmittedBy != nil && *query.SubmittedBy != uuid.Nil {
		db = db.Where("submitted_by = ?", *query.SubmittedBy)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var edits []model.MusicEdit
	err := db.Order("created_at DESC").Limit(query.PageSize).Offset((query.Page - 1) * query.PageSize).Find(&edits).Error
	return edits, total, err
}

func (r *Repo) UpsertArtistBookmark(userID uuid.UUID, artistID uuid.UUID) (model.ArtistBookmark, error) {
	bookmark := model.ArtistBookmark{UserID: userID, ArtistID: artistID}
	err := r.db.Where("user_id = ? AND artist_id = ?", userID, artistID).FirstOrCreate(&bookmark).Error
	return bookmark, err
}

func (r *Repo) ListArtistBookmarks(userID uuid.UUID, page int, pageSize int, sort string) ([]model.ArtistBookmark, int64, error) {
	return r.ListArtistBookmarksFiltered(userID, page, pageSize, sort, "", nil)
}

func (r *Repo) ListArtistBookmarksFiltered(userID uuid.UUID, page int, pageSize int, sort string, query string, viewer *authctx.CurrentUser) ([]model.ArtistBookmark, int64, error) {
	var total int64
	db := r.db.Model(&model.ArtistBookmark{}).
		Joins("JOIN \"Artists\" ON \"Artists\".id = music_artist_bookmarks.artist_id AND \"Artists\".lifecycle_status = 'active'").
		Where("music_artist_bookmarks.user_id = ?", userID)
	if query = strings.TrimSpace(query); query != "" {
		pattern := "%" + strings.ToLower(query) + "%"
		db = db.Where("LOWER(\"Artists\".name) LIKE ? OR LOWER(COALESCE(\"Artists\".legal_name, '')) LIKE ?", pattern, pattern)
	}
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var bookmarks []model.ArtistBookmark
	switch normalizeBookmarkSort(sort) {
	case BookmarkSortPopular:
		playCountSubquery := r.db.
			Table("song_artists").
			Select("song_artists.artist_id AS artist_id, COALESCE(SUM(\"Songs\".play_count), 0) AS play_count").
			Joins("JOIN \"Songs\" ON \"Songs\".id = song_artists.song_id AND \"Songs\".lifecycle_status = 'active'").
			Group("song_artists.artist_id")
		db = db.Joins("LEFT JOIN (?) AS artist_popularity ON artist_popularity.artist_id = music_artist_bookmarks.artist_id", playCountSubquery).
			Order("COALESCE(artist_popularity.play_count, 0) DESC").
			Order("music_artist_bookmarks.created_at DESC")
	case BookmarkSortName:
		db = db.Order("LOWER(\"Artists\".name) ASC").Order("music_artist_bookmarks.created_at DESC")
	default:
		db = db.Order("music_artist_bookmarks.created_at DESC")
	}
	err := db.Preload("Artist", visibleArtistPreload(viewer)).Limit(pageSize).Offset((page - 1) * pageSize).Find(&bookmarks).Error
	return bookmarks, total, err
}

func (r *Repo) DeleteArtistBookmark(userID uuid.UUID, artistID uuid.UUID) error {
	return r.db.Where("user_id = ? AND artist_id = ?", userID, artistID).Delete(&model.ArtistBookmark{}).Error
}

func (r *Repo) UpsertAlbumBookmark(userID uuid.UUID, albumID uuid.UUID) (model.AlbumBookmark, error) {
	bookmark := model.AlbumBookmark{UserID: userID, AlbumID: albumID}
	err := r.db.Where("user_id = ? AND album_id = ?", userID, albumID).FirstOrCreate(&bookmark).Error
	return bookmark, err
}

func (r *Repo) ListAlbumBookmarks(userID uuid.UUID, page int, pageSize int, sort string) ([]model.AlbumBookmark, int64, error) {
	return r.ListAlbumBookmarksFiltered(userID, page, pageSize, sort, "", nil)
}

func (r *Repo) ListAlbumBookmarksFiltered(userID uuid.UUID, page int, pageSize int, sort string, query string, viewer *authctx.CurrentUser) ([]model.AlbumBookmark, int64, error) {
	var total int64
	db := r.db.Model(&model.AlbumBookmark{}).
		Joins("JOIN \"Albums\" ON \"Albums\".id = music_album_bookmarks.album_id AND \"Albums\".lifecycle_status = 'active'").
		Where("music_album_bookmarks.user_id = ?", userID)
	if query = strings.TrimSpace(query); query != "" {
		db = db.Where("LOWER(\"Albums\".title) LIKE ?", "%"+strings.ToLower(query)+"%")
	}
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var bookmarks []model.AlbumBookmark
	switch normalizeBookmarkSort(sort) {
	case BookmarkSortPopular:
		db = db.Order("\"Albums\".hot_score DESC").
			Order("\"Albums\".play_count DESC").
			Order("music_album_bookmarks.created_at DESC")
	case BookmarkSortName:
		db = db.Order("LOWER(\"Albums\".title) ASC").Order("music_album_bookmarks.created_at DESC")
	default:
		db = db.Order("music_album_bookmarks.created_at DESC")
	}
	err := db.Preload("Album.Artists", visibleArtistPreload(viewer)).Limit(pageSize).Offset((page - 1) * pageSize).Find(&bookmarks).Error
	return bookmarks, total, err
}

func (r *Repo) ListLatestAlbumBookmarksAfter(userID uuid.UUID, pageSize int, cursor *musicCreatedAtCursor, viewer *authctx.CurrentUser) ([]model.AlbumBookmark, bool, error) {
	db := r.db.Model(&model.AlbumBookmark{}).
		Joins("JOIN \"Albums\" ON \"Albums\".id = music_album_bookmarks.album_id AND \"Albums\".lifecycle_status = 'active'").
		Where("music_album_bookmarks.user_id = ?", userID).
		Order("music_album_bookmarks.created_at DESC").
		Order("music_album_bookmarks.id DESC")
	if cursor != nil {
		db = db.Where("(music_album_bookmarks.created_at < ? OR (music_album_bookmarks.created_at = ? AND music_album_bookmarks.id < ?))", cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	var bookmarks []model.AlbumBookmark
	if err := db.Preload("Album.Artists", visibleArtistPreload(viewer)).Limit(pageSize + 1).Find(&bookmarks).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(bookmarks) > pageSize
	if hasMore {
		bookmarks = bookmarks[:pageSize]
	}
	return bookmarks, hasMore, nil
}

func (r *Repo) DeleteAlbumBookmark(userID uuid.UUID, albumID uuid.UUID) error {
	return r.db.Where("user_id = ? AND album_id = ?", userID, albumID).Delete(&model.AlbumBookmark{}).Error
}

func (r *Repo) UpsertPlaylistBookmark(userID uuid.UUID, playlistID uuid.UUID) (model.PlaylistBookmark, error) {
	bookmark := model.PlaylistBookmark{UserID: userID, PlaylistID: playlistID}
	if err := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&bookmark).Error; err != nil {
		return model.PlaylistBookmark{}, err
	}
	var stored model.PlaylistBookmark
	if err := r.db.Where("user_id = ? AND playlist_id = ?", userID, playlistID).First(&stored).Error; err != nil {
		return model.PlaylistBookmark{}, err
	}
	return stored, nil
}

func (r *Repo) ListPlaylistBookmarks(userID uuid.UUID, page int, pageSize int, sort string) ([]model.PlaylistBookmark, int64, error) {
	return r.ListPlaylistBookmarksFiltered(userID, page, pageSize, sort, "")
}

func (r *Repo) ListPlaylistBookmarksFiltered(userID uuid.UUID, page int, pageSize int, sort string, query string) ([]model.PlaylistBookmark, int64, error) {
	var total int64
	db := r.db.Model(&model.PlaylistBookmark{}).
		Joins("JOIN music_playlists ON music_playlists.id = music_playlist_bookmarks.playlist_id AND music_playlists.deleted_at IS NULL").
		Where("music_playlist_bookmarks.user_id = ? AND (music_playlists.user_id = ? OR music_playlists.is_public = ?)", userID, userID, true)
	if query = strings.TrimSpace(query); query != "" {
		db = db.Where("LOWER(music_playlists.name) LIKE ?", "%"+strings.ToLower(query)+"%")
	}
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var bookmarks []model.PlaylistBookmark
	switch normalizeBookmarkSort(sort) {
	case BookmarkSortPopular:
		songCountSubquery := r.db.Table("music_playlist_songs").
			Select("playlist_id, COUNT(*) AS song_count").
			Where("music_playlist_songs.deleted_at IS NULL").
			Group("playlist_id")
		db = db.
			Joins("LEFT JOIN (?) AS playlist_song_counts ON playlist_song_counts.playlist_id = music_playlist_bookmarks.playlist_id", songCountSubquery).
			Order("COALESCE(playlist_song_counts.song_count, 0) DESC").
			Order("music_playlist_bookmarks.created_at DESC")
	case BookmarkSortName:
		db = db.Order("LOWER(music_playlists.name) ASC").Order("music_playlist_bookmarks.created_at DESC")
	default:
		db = db.Order("music_playlist_bookmarks.created_at DESC")
	}
	if err := db.Preload("Playlist.User").Limit(pageSize).Offset((page - 1) * pageSize).Find(&bookmarks).Error; err != nil {
		return nil, 0, err
	}
	playlistIDs := make([]uuid.UUID, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		playlistIDs = append(playlistIDs, bookmark.PlaylistID)
	}
	songCounts, err := r.CountPlaylistSongs(playlistIDs)
	if err != nil {
		return nil, 0, err
	}
	for index := range bookmarks {
		if bookmarks[index].Playlist == nil {
			continue
		}
		bookmarks[index].Playlist.SongCount = songCounts[bookmarks[index].PlaylistID]
		if bookmarks[index].Playlist.User != nil {
			bookmarks[index].Playlist.OwnerUsername = bookmarks[index].Playlist.User.Username
		}
	}
	return bookmarks, total, nil
}

func (r *Repo) ListPlaylistSongsFiltered(playlistID uuid.UUID, page int, pageSize int, sort string, query string, viewer *authctx.CurrentUser) ([]model.PlaylistSong, int64, error) {
	var total int64
	db := r.db.Model(&model.PlaylistSong{}).
		Joins("JOIN \"Songs\" ON \"Songs\".id = music_playlist_songs.song_id AND \"Songs\".lifecycle_status = 'active'").
		Where("music_playlist_songs.playlist_id = ?", playlistID)
	if query = strings.TrimSpace(query); query != "" {
		db = db.Where("LOWER(\"Songs\".title) LIKE ?", "%"+strings.ToLower(query)+"%")
	}
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	switch normalizeBookmarkSort(sort) {
	case BookmarkSortPopular:
		db = db.Order("\"Songs\".play_count DESC").Order("music_playlist_songs.created_at DESC")
	case BookmarkSortName:
		db = db.Order("LOWER(\"Songs\".title) ASC").Order("music_playlist_songs.created_at DESC")
	default:
		db = db.Order("music_playlist_songs.created_at DESC")
	}
	var songs []model.PlaylistSong
	err := db.Preload("Song.Artists", visibleArtistPreload(viewer)).Preload("Song.Album", visibleAlbumPreload(viewer)).Limit(pageSize).Offset((page - 1) * pageSize).Find(&songs).Error
	return songs, total, err
}

func (r *Repo) DeletePlaylistBookmark(userID uuid.UUID, playlistID uuid.UUID) error {
	return r.db.Where("user_id = ? AND playlist_id = ?", userID, playlistID).Delete(&model.PlaylistBookmark{}).Error
}

func (r *Repo) CreateArtist(artist model.Artist) (model.Artist, error) {
	return artist, r.db.Create(&artist).Error
}

func (r *Repo) GetArtist(artistID uuid.UUID) (model.Artist, error) {
	var artist model.Artist
	err := r.db.First(&artist, "id = ?", artistID).Error
	return artist, err
}

func (r *Repo) CreatePlaylist(playlist model.Playlist) (model.Playlist, error) {
	return playlist, r.db.Create(&playlist).Error
}

func (r *Repo) EnsureFavoritePlaylist(userID uuid.UUID) (model.Playlist, error) {
	playlist := model.Playlist{UserID: userID, Name: "最爱", Kind: "favorite", IsPublic: false}
	err := r.db.Where("user_id = ? AND kind = ?", userID, "favorite").FirstOrCreate(&playlist).Error
	return playlist, err
}

func (r *Repo) ListPlaylists(userID uuid.UUID, page int, pageSize int, sort string) ([]model.Playlist, int64, error) {
	var total int64
	db := r.db.Model(&model.Playlist{}).Where("user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var playlists []model.Playlist
	db = db.Order("CASE WHEN music_playlists.kind = 'favorite' THEN 0 ELSE 1 END ASC")
	if normalizeBookmarkSort(sort) == BookmarkSortPopular {
		songCountSubquery := r.db.
			Table("music_playlist_songs").
			Select("playlist_id, COUNT(*) AS song_count").
			Group("playlist_id")
		db = db.
			Joins("LEFT JOIN (?) AS playlist_song_counts ON playlist_song_counts.playlist_id = music_playlists.id", songCountSubquery).
			Order("COALESCE(playlist_song_counts.song_count, 0) DESC").
			Order("music_playlists.created_at DESC")
	} else {
		db = db.Order("music_playlists.created_at DESC")
	}
	err := db.Limit(pageSize).Offset((page - 1) * pageSize).Find(&playlists).Error
	return playlists, total, err
}

func (r *Repo) ListPublicPlaylists(page int, pageSize int) ([]model.Playlist, int64, error) {
	var total int64
	db := r.db.Model(&model.Playlist{}).Where("is_public = ? AND kind = ?", true, "user")
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var playlists []model.Playlist
	err := db.Order("created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&playlists).Error
	return playlists, total, err
}

func (r *Repo) ListRecentPublicPlaylists(limit int) ([]model.Playlist, int64, error) {
	if limit < 1 {
		limit = 1
	}
	var total int64
	base := r.db.Model(&model.Playlist{}).Where("is_public = ? AND kind = ?", true, "user")
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var playlists []model.Playlist
	err := base.
		Order("created_at DESC").
		Limit(limit).
		Find(&playlists).Error
	return playlists, total, err
}

func (r *Repo) CountPlaylistSongs(playlistIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	counts := make(map[uuid.UUID]int64, len(playlistIDs))
	if len(playlistIDs) == 0 {
		return counts, nil
	}

	var rows []struct {
		PlaylistID uuid.UUID
		Count      int64
	}
	if err := r.db.Model(&model.PlaylistSong{}).
		Select("music_playlist_songs.playlist_id, COUNT(*) AS count").
		Joins("JOIN \"Songs\" ON \"Songs\".id = music_playlist_songs.song_id AND \"Songs\".deleted_at IS NULL AND \"Songs\".lifecycle_status = ?", model.MusicLifecycleActive).
		Where("music_playlist_songs.playlist_id IN ?", playlistIDs).
		Group("music_playlist_songs.playlist_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts[row.PlaylistID] = row.Count
	}
	return counts, nil
}

func (r *Repo) DeletePlaylist(userID uuid.UUID, playlistID uuid.UUID) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("playlist_id = ?", playlistID).Delete(&model.PlaylistSong{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("playlist_id = ?", playlistID).Delete(&model.PlaylistBookmark{}).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ? AND id = ?", userID, playlistID).Delete(&model.Playlist{}).Error
	})
}

func (r *Repo) GetPlaylistForUser(userID uuid.UUID, playlistID uuid.UUID) (model.Playlist, error) {
	var playlist model.Playlist
	err := r.db.First(&playlist, "user_id = ? AND id = ?", userID, playlistID).Error
	return playlist, err
}

func (r *Repo) GetPlaylistByID(playlistID uuid.UUID) (model.Playlist, error) {
	var playlist model.Playlist
	err := r.db.First(&playlist, "id = ?", playlistID).Error
	return playlist, err
}

func (r *Repo) UpdatePlaylist(playlist *model.Playlist, updates map[string]any) error {
	return r.db.Model(playlist).Updates(updates).Error
}

func (r *Repo) UpsertPlaylistSong(playlistID uuid.UUID, songID uuid.UUID) (model.PlaylistSong, error) {
	var playlistSong model.PlaylistSong
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").First(&model.Playlist{}, "id = ?", playlistID).Error; err != nil {
			return err
		}
		if err := tx.Where("playlist_id = ? AND song_id = ?", playlistID, songID).First(&playlistSong).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var maxPosition int
		if err := tx.Model(&model.PlaylistSong{}).Where("playlist_id = ?", playlistID).
			Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error; err != nil {
			return err
		}
		playlistSong = model.PlaylistSong{PlaylistID: playlistID, SongID: songID, Position: maxPosition + 1}
		return tx.Create(&playlistSong).Error
	})
	return playlistSong, err
}

func (r *Repo) ListPlaylistSongs(playlistID uuid.UUID, page int, pageSize int, viewer *authctx.CurrentUser) ([]model.PlaylistSong, int64, error) {
	base := r.db.Model(&model.PlaylistSong{}).
		Joins("JOIN \"Songs\" AS visible_song ON visible_song.id = music_playlist_songs.song_id AND visible_song.deleted_at IS NULL AND visible_song.lifecycle_status = ?", model.MusicLifecycleActive).
		Where("music_playlist_songs.playlist_id = ?", playlistID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var songs []model.PlaylistSong
	err := base.Preload("Song.Artists", visibleArtistPreload(viewer)).Preload("Song.Album", visibleAlbumPreload(viewer)).Order("music_playlist_songs.position ASC, music_playlist_songs.created_at ASC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&songs).Error
	return songs, total, err
}

func (r *Repo) ReorderPlaylistSongs(playlistID uuid.UUID, songIDs []uuid.UUID) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for index, songID := range songIDs {
			result := tx.Model(&model.PlaylistSong{}).
				Where("playlist_id = ? AND song_id = ?", playlistID, songID).
				Update("position", index+1)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
		}
		return nil
	})
}

func (r *Repo) DeletePlaylistSong(playlistID uuid.UUID, songID uuid.UUID) error {
	return r.db.Where("playlist_id = ? AND song_id = ?", playlistID, songID).Delete(&model.PlaylistSong{}).Error
}

func (r *Repo) IncrementSongPlayCount(songID uuid.UUID) error {
	return r.db.Model(&model.Song{}).
		Where("id = ?", songID).
		UpdateColumn("play_count", gorm.Expr("play_count + 1")).
		Error
}

func (r *Repo) RecordListeningHistory(userID, songID uuid.UUID, playedAt time.Time) error {
	history := model.MusicListeningHistory{
		UserID: userID, SongID: songID, PlayCount: 1, LastPlayedAt: playedAt,
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:     []clause.Column{{Name: "user_id"}, {Name: "song_id"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "deleted_at"}, Value: nil}}},
		DoUpdates: clause.Assignments(map[string]any{
			"play_count":     gorm.Expr("music_listening_histories.play_count + 1"),
			"last_played_at": playedAt,
			"updated_at":     playedAt,
		}),
	}).Create(&history).Error
}

func (r *Repo) ListRecentListeningHistory(userID uuid.UUID, limit int, viewer *authctx.CurrentUser) ([]model.MusicListeningHistory, error) {
	if limit < 1 {
		return []model.MusicListeningHistory{}, nil
	}
	var rows []model.MusicListeningHistory
	err := r.db.Model(&model.MusicListeningHistory{}).
		Joins("JOIN \"Songs\" AS visible_song ON visible_song.id = music_listening_histories.song_id AND visible_song.deleted_at IS NULL AND visible_song.lifecycle_status = ?", model.MusicLifecycleActive).
		Where("music_listening_histories.user_id = ?", userID).
		Preload("Song.Artists", visibleArtistPreload(viewer)).
		Preload("Song.Album", visibleAlbumPreload(viewer)).
		Order("music_listening_histories.last_played_at DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

func (r *Repo) ListListeningHistory(userID uuid.UUID, page, pageSize int, viewer *authctx.CurrentUser) ([]model.MusicListeningHistory, int64, error) {
	base := r.db.Model(&model.MusicListeningHistory{}).
		Joins("JOIN \"Songs\" AS visible_song ON visible_song.id = music_listening_histories.song_id AND visible_song.deleted_at IS NULL AND visible_song.lifecycle_status = ?", model.MusicLifecycleActive).
		Where("music_listening_histories.user_id = ?", userID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.MusicListeningHistory
	err := base.Preload("Song.Artists", visibleArtistPreload(viewer)).Preload("Song.Album", visibleAlbumPreload(viewer)).
		Order("music_listening_histories.last_played_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&rows).Error
	return rows, total, err
}

func (r *Repo) ClearListeningHistory(userID uuid.UUID) error {
	return r.db.Where("user_id = ?", userID).Delete(&model.MusicListeningHistory{}).Error
}
