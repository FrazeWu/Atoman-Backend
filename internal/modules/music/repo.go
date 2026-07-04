package music

import (
	"errors"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

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

func (r *Repo) ListArtistBookmarks(userID uuid.UUID, page int, pageSize int) ([]model.ArtistBookmark, int64, error) {
	var total int64
	db := r.db.Model(&model.ArtistBookmark{}).Where("user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var bookmarks []model.ArtistBookmark
	err := db.Preload("Artist").Order("created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&bookmarks).Error
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

func (r *Repo) ListAlbumBookmarks(userID uuid.UUID, page int, pageSize int) ([]model.AlbumBookmark, int64, error) {
	var total int64
	db := r.db.Model(&model.AlbumBookmark{}).Where("user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var bookmarks []model.AlbumBookmark
	err := db.Preload("Album.Artists").Preload("Album.Songs").Order("created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&bookmarks).Error
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

func (r *Repo) ListSongBookmarks(userID uuid.UUID, page int, pageSize int) ([]model.SongBookmark, int64, error) {
	var total int64
	db := r.db.Model(&model.SongBookmark{}).Where("user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var bookmarks []model.SongBookmark
	err := db.Preload("Song.Artists").Preload("Song.Album").Order("created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&bookmarks).Error
	return bookmarks, total, err
}

func (r *Repo) DeleteSongBookmark(userID uuid.UUID, songID uuid.UUID) error {
	return r.db.Where("user_id = ? AND song_id = ?", userID, songID).Delete(&model.SongBookmark{}).Error
}

func (r *Repo) CreatePlaylist(userID uuid.UUID, name string) (model.Playlist, error) {
	playlist := model.Playlist{UserID: userID, Name: name}
	return playlist, r.db.Create(&playlist).Error
}

func (r *Repo) ListPlaylists(userID uuid.UUID, page int, pageSize int) ([]model.Playlist, int64, error) {
	var total int64
	db := r.db.Model(&model.Playlist{}).Where("user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var playlists []model.Playlist
	err := db.Order("created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&playlists).Error
	return playlists, total, err
}

func (r *Repo) ListPublicPlaylists(page int, pageSize int) ([]model.Playlist, int64, error) {
	var total int64
	db := r.db.Model(&model.Playlist{}).Where("is_public = ?", true)
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
	base := r.db.Model(&model.Playlist{}).Where("is_public = ?", true)
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
	return r.db.Where("user_id = ? AND id = ?", userID, playlistID).Delete(&model.Playlist{}).Error
}

func (r *Repo) GetPlaylistForUser(userID uuid.UUID, playlistID uuid.UUID) (model.Playlist, error) {
	var playlist model.Playlist
	err := r.db.First(&playlist, "user_id = ? AND id = ?", userID, playlistID).Error
	return playlist, err
}

func (r *Repo) UpsertPlaylistSong(playlistID uuid.UUID, songID uuid.UUID) (model.PlaylistSong, error) {
	playlistSong := model.PlaylistSong{PlaylistID: playlistID, SongID: songID}
	err := r.db.Where("playlist_id = ? AND song_id = ?", playlistID, songID).FirstOrCreate(&playlistSong).Error
	return playlistSong, err
}

func (r *Repo) ListPlaylistSongs(playlistID uuid.UUID, page int, pageSize int) ([]model.PlaylistSong, int64, error) {
	var total int64
	db := r.db.Model(&model.PlaylistSong{}).Where("playlist_id = ?", playlistID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var songs []model.PlaylistSong
	err := db.Preload("Song.Artists").Preload("Song.Album").Order("created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&songs).Error
	return songs, total, err
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
