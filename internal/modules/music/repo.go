package music

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"

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
)

func normalizeBookmarkSort(sort string) BookmarkSort {
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
	var total int64
	db := r.db.Model(&model.ArtistBookmark{}).Where("user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var bookmarks []model.ArtistBookmark
	if normalizeBookmarkSort(sort) == BookmarkSortPopular {
		playCountSubquery := r.db.
			Table("song_artists").
			Select("song_artists.artist_id AS artist_id, COALESCE(SUM(\"Songs\".play_count), 0) AS play_count").
			Joins("JOIN \"Songs\" ON \"Songs\".id = song_artists.song_id").
			Group("song_artists.artist_id")
		db = db.
			Joins("JOIN \"Artists\" ON \"Artists\".id = music_artist_bookmarks.artist_id").
			Joins("LEFT JOIN (?) AS artist_popularity ON artist_popularity.artist_id = music_artist_bookmarks.artist_id", playCountSubquery).
			Order("COALESCE(artist_popularity.play_count, 0) DESC").
			Order("music_artist_bookmarks.created_at DESC")
	} else {
		db = db.Order("music_artist_bookmarks.created_at DESC")
	}
	err := db.Preload("Artist").Limit(pageSize).Offset((page - 1) * pageSize).Find(&bookmarks).Error
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
	var total int64
	db := r.db.Model(&model.AlbumBookmark{}).Where("user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var bookmarks []model.AlbumBookmark
	if normalizeBookmarkSort(sort) == BookmarkSortPopular {
		db = db.
			Joins("JOIN \"Albums\" ON \"Albums\".id = music_album_bookmarks.album_id").
			Order("\"Albums\".hot_score DESC").
			Order("\"Albums\".play_count DESC").
			Order("music_album_bookmarks.created_at DESC")
	} else {
		db = db.Order("music_album_bookmarks.created_at DESC")
	}
	err := db.Preload("Album.Artists").Preload("Album.Songs").Limit(pageSize).Offset((page - 1) * pageSize).Find(&bookmarks).Error
	return bookmarks, total, err
}

func (r *Repo) DeleteAlbumBookmark(userID uuid.UUID, albumID uuid.UUID) error {
	return r.db.Where("user_id = ? AND album_id = ?", userID, albumID).Delete(&model.AlbumBookmark{}).Error
}

func (r *Repo) UpsertSongBookmark(userID uuid.UUID, songID uuid.UUID) (model.SongBookmark, error) {
	bookmark := model.SongBookmark{UserID: userID, SongID: songID}
	err := r.db.Where("user_id = ? AND song_id = ?", userID, songID).FirstOrCreate(&bookmark).Error
	return bookmark, err
}

func (r *Repo) ListSongBookmarks(userID uuid.UUID, page int, pageSize int, sort string) ([]model.SongBookmark, int64, error) {
	var total int64
	db := r.db.Model(&model.SongBookmark{}).Where("user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var bookmarks []model.SongBookmark
	if normalizeBookmarkSort(sort) == BookmarkSortPopular {
		db = db.
			Joins("JOIN \"Songs\" ON \"Songs\".id = music_song_bookmarks.song_id").
			Order("\"Songs\".play_count DESC").
			Order("music_song_bookmarks.created_at DESC")
	} else {
		db = db.Order("music_song_bookmarks.created_at DESC")
	}
	err := db.Preload("Song.Artists").Preload("Song.Album").Limit(pageSize).Offset((page - 1) * pageSize).Find(&bookmarks).Error
	return bookmarks, total, err
}

func (r *Repo) DeleteSongBookmark(userID uuid.UUID, songID uuid.UUID) error {
	return r.db.Where("user_id = ? AND song_id = ?", userID, songID).Delete(&model.SongBookmark{}).Error
}

func (r *Repo) UpsertPlaylistBookmark(userID uuid.UUID, playlistID uuid.UUID) (model.PlaylistBookmark, error) {
	bookmark := model.PlaylistBookmark{UserID: userID, PlaylistID: playlistID}
	err := r.db.Where("user_id = ? AND playlist_id = ?", userID, playlistID).FirstOrCreate(&bookmark).Error
	return bookmark, err
}

func (r *Repo) ListPlaylistBookmarks(userID uuid.UUID, page int, pageSize int, sort string) ([]model.PlaylistBookmark, int64, error) {
	var total int64
	db := r.db.Model(&model.PlaylistBookmark{}).Where("music_playlist_bookmarks.user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var bookmarks []model.PlaylistBookmark
	if normalizeBookmarkSort(sort) == BookmarkSortPopular {
		songCountSubquery := r.db.Table("music_playlist_songs").
			Select("playlist_id, COUNT(*) AS song_count").
			Group("playlist_id")
		db = db.
			Joins("LEFT JOIN (?) AS playlist_song_counts ON playlist_song_counts.playlist_id = music_playlist_bookmarks.playlist_id", songCountSubquery).
			Order("COALESCE(playlist_song_counts.song_count, 0) DESC").
			Order("music_playlist_bookmarks.created_at DESC")
	} else {
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

func (r *Repo) UpdateArtist(artist *model.Artist, updates map[string]any) error {
	return r.db.Model(artist).Updates(updates).Error
}

func (r *Repo) CreatePlaylist(playlist model.Playlist) (model.Playlist, error) {
	return playlist, r.db.Create(&playlist).Error
}

func (r *Repo) ListPlaylists(userID uuid.UUID, page int, pageSize int, sort string) ([]model.Playlist, int64, error) {
	var total int64
	db := r.db.Model(&model.Playlist{}).Where("user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var playlists []model.Playlist
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
	db := r.db.Model(&model.Playlist{}).Where("is_public = ? AND is_favorite = ?", true, false)
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
	base := r.db.Model(&model.Playlist{}).Where("is_public = ? AND is_favorite = ?", true, false)
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
		Select("playlist_id, COUNT(*) AS count").
		Where("playlist_id IN ?", playlistIDs).
		Group("playlist_id").
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

func (r *Repo) ListPlaylistSongs(playlistID uuid.UUID, page int, pageSize int) ([]model.PlaylistSong, int64, error) {
	var total int64
	db := r.db.Model(&model.PlaylistSong{}).Where("playlist_id = ?", playlistID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var songs []model.PlaylistSong
	err := db.Preload("Song.Artists").Preload("Song.Album").Order("position ASC, created_at ASC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&songs).Error
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
	var history model.MusicListeningHistory
	err := r.db.Where("user_id = ? AND song_id = ?", userID, songID).First(&history).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.Create(&model.MusicListeningHistory{
			UserID:       userID,
			SongID:       songID,
			PlayCount:    1,
			LastPlayedAt: playedAt,
		}).Error
	}
	if err != nil {
		return err
	}
	return r.db.Model(&history).Updates(map[string]any{
		"play_count":     gorm.Expr("play_count + 1"),
		"last_played_at": playedAt,
	}).Error
}

func (r *Repo) ListListeningHistory(userID uuid.UUID, page, pageSize int) ([]model.MusicListeningHistory, int64, error) {
	db := r.db.Model(&model.MusicListeningHistory{}).Where("user_id = ?", userID)
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.MusicListeningHistory
	err := db.Preload("Song.Artists").Preload("Song.Album").
		Order("last_played_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&rows).Error
	return rows, total, err
}
