package music

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

func (s *Service) ListArtistBookmarks(user authctx.CurrentUser, page int, pageSize int, sort string) ([]model.ArtistBookmark, int64, error) {
	return s.ListArtistBookmarksFiltered(user, page, pageSize, sort, "")
}

func (s *Service) ListArtistBookmarksFiltered(user authctx.CurrentUser, page int, pageSize int, sort string, query string) ([]model.ArtistBookmark, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListArtistBookmarksFiltered(user.ID, page, pageSize, sort, query)
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
	return s.ListAlbumBookmarksFiltered(user, page, pageSize, sort, "")
}

func (s *Service) ListAlbumBookmarksFiltered(user authctx.CurrentUser, page int, pageSize int, sort string, query string) ([]model.AlbumBookmark, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListAlbumBookmarksFiltered(user.ID, page, pageSize, sort, query)
}

func (s *Service) ListLatestAlbumBookmarksAfter(user authctx.CurrentUser, pageSize int, cursor *musicCreatedAtCursor) ([]model.AlbumBookmark, bool, error) {
	if user.ID == uuid.Nil {
		return nil, false, apperr.Unauthorized("Login required")
	}
	_, pageSize = normalizeMusicRecommendationPage(1, pageSize)
	return s.repo.ListLatestAlbumBookmarksAfter(user.ID, pageSize, cursor)
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

func (s *Service) ListPlaylistBookmarks(user authctx.CurrentUser, page int, pageSize int, sort string) ([]model.PlaylistBookmark, int64, error) {
	return s.ListPlaylistBookmarksFiltered(user, page, pageSize, sort, "")
}

func (s *Service) ListPlaylistBookmarksFiltered(user authctx.CurrentUser, page int, pageSize int, sort string, query string) ([]model.PlaylistBookmark, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListPlaylistBookmarksFiltered(user.ID, page, pageSize, sort, query)
}

func (s *Service) ListLaterSongs(user authctx.CurrentUser, page int, pageSize int, sort string, query string) ([]model.PlaylistSong, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	var playlist model.Playlist
	if err := s.db.Where("user_id = ? AND kind = ?", user.ID, "later").First(&playlist).Error; err != nil {
		return []model.PlaylistSong{}, 0, nil
	}
	return s.repo.ListPlaylistSongsFiltered(playlist.ID, page, pageSize, sort, query)
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
