package music

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

func (s *Service) ListArtistBookmarks(user authctx.CurrentUser, page int, pageSize int, sort string) ([]model.ArtistBookmark, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListArtistBookmarks(user.ID, page, pageSize, sort)
}

func (s *Service) BookmarkArtist(user authctx.CurrentUser, artistID uuid.UUID) (model.ArtistBookmark, error) {
	if user.ID == uuid.Nil {
		return model.ArtistBookmark{}, apperr.Unauthorized("Login required")
	}
	if artistID == uuid.Nil {
		return model.ArtistBookmark{}, apperr.BadRequest("validation.invalid_request", "artist_id is required")
	}
	return s.repo.UpsertArtistBookmark(user.ID, artistID)
}

func (s *Service) DeleteArtistBookmark(user authctx.CurrentUser, artistID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteArtistBookmark(user.ID, artistID)
}

func (s *Service) ListAlbumBookmarks(user authctx.CurrentUser, page int, pageSize int, sort string) ([]model.AlbumBookmark, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListAlbumBookmarks(user.ID, page, pageSize, sort)
}

func (s *Service) BookmarkAlbum(user authctx.CurrentUser, albumID uuid.UUID) (model.AlbumBookmark, error) {
	if user.ID == uuid.Nil {
		return model.AlbumBookmark{}, apperr.Unauthorized("Login required")
	}
	if albumID == uuid.Nil {
		return model.AlbumBookmark{}, apperr.BadRequest("validation.invalid_request", "album_id is required")
	}
	return s.repo.UpsertAlbumBookmark(user.ID, albumID)
}

func (s *Service) DeleteAlbumBookmark(user authctx.CurrentUser, albumID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteAlbumBookmark(user.ID, albumID)
}

func (s *Service) ListSongBookmarks(user authctx.CurrentUser, page int, pageSize int, sort string) ([]model.SongBookmark, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListSongBookmarks(user.ID, page, pageSize, sort)
}

func (s *Service) BookmarkSong(user authctx.CurrentUser, songID uuid.UUID) (model.SongBookmark, error) {
	if user.ID == uuid.Nil {
		return model.SongBookmark{}, apperr.Unauthorized("Login required")
	}
	if songID == uuid.Nil {
		return model.SongBookmark{}, apperr.BadRequest("validation.invalid_request", "song_id is required")
	}
	return s.repo.UpsertSongBookmark(user.ID, songID)
}

func (s *Service) DeleteSongBookmark(user authctx.CurrentUser, songID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteSongBookmark(user.ID, songID)
}

func (s *Service) ListPlaylistBookmarks(user authctx.CurrentUser, page int, pageSize int, sort string) ([]model.PlaylistBookmark, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListPlaylistBookmarks(user.ID, page, pageSize, sort)
}

func (s *Service) BookmarkPlaylist(user authctx.CurrentUser, playlistID uuid.UUID) (model.PlaylistBookmark, error) {
	if user.ID == uuid.Nil {
		return model.PlaylistBookmark{}, apperr.Unauthorized("Login required")
	}
	if playlistID == uuid.Nil {
		return model.PlaylistBookmark{}, apperr.BadRequest("validation.invalid_request", "playlist_id is required")
	}
	if _, err := s.getVisiblePlaylist(user.ID, playlistID); err != nil {
		return model.PlaylistBookmark{}, err
	}
	return s.repo.UpsertPlaylistBookmark(user.ID, playlistID)
}

func (s *Service) DeletePlaylistBookmark(user authctx.CurrentUser, playlistID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeletePlaylistBookmark(user.ID, playlistID)
}
