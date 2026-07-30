package music

import (
	"errors"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) CreatePlaylist(user authctx.CurrentUser, req CreatePlaylistRequest) (model.Playlist, error) {
	if user.ID == uuid.Nil {
		return model.Playlist{}, apperr.Unauthorized("Login required")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return model.Playlist{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}

	playlist := model.Playlist{
		UserID:      user.ID,
		Name:        name,
		Description: strings.TrimSpace(req.Description),
		CoverURL:    strings.TrimSpace(req.CoverURL),
		IsPublic:    req.IsPublic,
	}
	return s.repo.CreatePlaylist(playlist)
}

func (s *Service) ListPlaylists(user authctx.CurrentUser, page int, pageSize int, sort string) ([]model.Playlist, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListPlaylists(user.ID, page, pageSize, sort)
}

func (s *Service) DeletePlaylist(user authctx.CurrentUser, playlistID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	playlist, err := s.repo.GetPlaylistForUser(user.ID, playlistID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.playlist_not_found", "Playlist not found")
		}
		return err
	}
	if playlist.IsFavorite {
		return apperr.Conflict("music.favorite_playlist_protected", "Favorite playlist cannot be deleted")
	}
	return s.repo.DeletePlaylist(user.ID, playlistID)
}

func (s *Service) UpdatePlaylist(user authctx.CurrentUser, playlistID uuid.UUID, req UpdatePlaylistRequest) (model.Playlist, error) {
	if user.ID == uuid.Nil {
		return model.Playlist{}, apperr.Unauthorized("Login required")
	}
	playlist, err := s.repo.GetPlaylistForUser(user.ID, playlistID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Playlist{}, apperr.NotFound("music.playlist_not_found", "Playlist not found")
		}
		return model.Playlist{}, err
	}

	updates := map[string]any{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return model.Playlist{}, apperr.BadRequest("validation.invalid_request", "name is required")
		}
		if playlist.IsFavorite && name != playlist.Name {
			return model.Playlist{}, apperr.Conflict("music.favorite_playlist_protected", "Favorite playlist cannot be renamed")
		}
		updates["name"] = name
	}
	if req.Description != nil {
		updates["description"] = strings.TrimSpace(*req.Description)
	}
	if req.CoverURL != nil {
		updates["cover_url"] = strings.TrimSpace(*req.CoverURL)
	}
	if req.IsPublic != nil {
		if playlist.IsFavorite && *req.IsPublic {
			return model.Playlist{}, apperr.Conflict("music.favorite_playlist_protected", "Favorite playlist cannot be public")
		}
		updates["is_public"] = *req.IsPublic
	}
	if len(updates) == 0 {
		return playlist, nil
	}

	if err := s.repo.UpdatePlaylist(&playlist, updates); err != nil {
		return model.Playlist{}, err
	}
	return s.repo.GetPlaylistForUser(user.ID, playlistID)
}

func (s *Service) GetPlaylist(user authctx.CurrentUser, playlistID uuid.UUID) (model.Playlist, error) {
	return s.getVisiblePlaylist(user.ID, playlistID)
}

func (s *Service) getVisiblePlaylist(userID uuid.UUID, playlistID uuid.UUID) (model.Playlist, error) {
	playlist, err := s.repo.GetPlaylistByID(playlistID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Playlist{}, apperr.NotFound("music.playlist_not_found", "Playlist not found")
		}
		return model.Playlist{}, err
	}
	if playlist.UserID != userID && !playlist.IsPublic {
		return model.Playlist{}, apperr.NotFound("music.playlist_not_found", "Playlist not found")
	}
	return playlist, nil
}

func (s *Service) AddPlaylistSong(user authctx.CurrentUser, playlistID uuid.UUID, songID uuid.UUID) (model.PlaylistSong, error) {
	if user.ID == uuid.Nil {
		return model.PlaylistSong{}, apperr.Unauthorized("Login required")
	}
	if songID == uuid.Nil {
		return model.PlaylistSong{}, apperr.BadRequest("validation.invalid_request", "song_id is required")
	}
	if _, err := s.repo.GetPlaylistForUser(user.ID, playlistID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.PlaylistSong{}, apperr.NotFound("music.playlist_not_found", "Playlist not found")
		}
		return model.PlaylistSong{}, err
	}
	return s.repo.UpsertPlaylistSong(playlistID, songID)
}

func (s *Service) ListPlaylistSongs(user authctx.CurrentUser, playlistID uuid.UUID, page int, pageSize int) ([]model.PlaylistSong, int64, error) {
	playlist, err := s.repo.GetPlaylistByID(playlistID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, apperr.NotFound("music.playlist_not_found", "Playlist not found")
		}
		return nil, 0, err
	}
	if playlist.UserID != user.ID && !playlist.IsPublic {
		return nil, 0, apperr.NotFound("music.playlist_not_found", "Playlist not found")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListPlaylistSongs(playlistID, page, pageSize)
}

func (s *Service) DeletePlaylistSong(user authctx.CurrentUser, playlistID uuid.UUID, songID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if _, err := s.repo.GetPlaylistForUser(user.ID, playlistID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.playlist_not_found", "Playlist not found")
		}
		return err
	}
	return s.repo.DeletePlaylistSong(playlistID, songID)
}

func (s *Service) ReorderPlaylistSongs(user authctx.CurrentUser, playlistID uuid.UUID, songIDs []uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if _, err := s.repo.GetPlaylistForUser(user.ID, playlistID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.playlist_not_found", "Playlist not found")
		}
		return err
	}
	var rows []model.PlaylistSong
	if err := s.db.Where("playlist_id = ?", playlistID).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(songIDs) {
		return apperr.BadRequest("validation.invalid_request", "song_ids must contain every playlist song")
	}
	existing := make(map[uuid.UUID]bool, len(rows))
	for _, row := range rows {
		existing[row.SongID] = true
	}
	seen := make(map[uuid.UUID]bool, len(songIDs))
	for _, songID := range songIDs {
		if songID == uuid.Nil || !existing[songID] || seen[songID] {
			return apperr.BadRequest("validation.invalid_request", "song_ids must contain every playlist song once")
		}
		seen[songID] = true
	}
	return s.repo.ReorderPlaylistSongs(playlistID, songIDs)
}

func (s *Service) ListPublicPlaylists(page int, pageSize int) ([]model.Playlist, map[uuid.UUID]int64, int64, error) {
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)

	playlists, total, err := s.repo.ListPublicPlaylists(page, pageSize)
	if err != nil {
		return nil, nil, 0, err
	}

	playlistIDs := make([]uuid.UUID, 0, len(playlists))
	for _, playlist := range playlists {
		playlistIDs = append(playlistIDs, playlist.ID)
	}
	songCounts, err := s.repo.CountPlaylistSongs(playlistIDs)
	if err != nil {
		return nil, nil, 0, err
	}

	return playlists, songCounts, total, nil
}
