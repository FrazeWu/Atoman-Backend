package music

import (
	"errors"
	"path"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	revisionservice "atoman/internal/service"
	"atoman/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) ConvertStandaloneSongToAlbum(user authctx.CurrentUser, songID uuid.UUID, input MusicReleaseConversionRequest) (uuid.UUID, error) {
	if user.ID == uuid.Nil {
		return uuid.Nil, apperr.Unauthorized("Login required")
	}
	albumType := strings.ToLower(strings.TrimSpace(input.ReleaseType))
	if albumType == "" || albumType == "single" || albumType == "leak" {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", "target album type is required")
	}
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.CoverURL) == "" || strings.TrimSpace(input.ReleaseDate) == "" {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", "title, cover and release_date are required")
	}
	sources, sourcesJSON, err := normalizeMusicSources(input.Sources, "")
	if err != nil {
		return uuid.Nil, err
	}
	if len(sources) == 0 {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", "album sources are required")
	}
	releaseDate, precision, err := parseOptionalReleaseDate(input.ReleaseDate)
	if err != nil {
		return uuid.Nil, err
	}
	if releaseDate == nil {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", "release_date is required")
	}

	albumID := uuid.New()
	promoted, err := storage.PromoteMusicUploadAsset(
		s.s3, input.CoverURL,
		storage.BuildMusicAlbumCoverVersionKey(albumID.String(), uuid.NewString(), path.Ext(input.CoverURL)),
	)
	if err != nil {
		return uuid.Nil, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var song model.Song
		if err := tx.First(&song, "id = ?", songID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.song_not_found", "Song not found")
			}
			return err
		}
		if song.AlbumID != nil || song.ReleaseType == nil {
			return apperr.BadRequest("validation.invalid_request", "song is not standalone")
		}
		if err := validateReleaseConversionProtection(tx, "song", song.ID, user.Role); err != nil {
			return err
		}
		if err := revisionservice.ValidateMusicEntryEdit(tx, "song", song.ID, user.ID); err != nil {
			return err
		}
		revisions := revisionservice.NewRevisionService(tx)
		if _, err := revisions.EnsureInitialRevision("song", song.ID, user.ID); err != nil {
			return err
		}

		album := model.Album{
			Base: model.Base{ID: albumID}, Title: strings.TrimSpace(input.Title), Description: strings.TrimSpace(input.Description),
			ReleaseDate: *releaseDate, ReleaseDatePrecision: precision, ReleaseYear: releaseDate.Year(), Year: releaseDate.Year(),
			CoverURL: promoted.URL, CoverSource: coverSourceFromURL(promoted.URL), AlbumType: albumType,
			Status: "open", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive,
			EditStatus: model.MusicEditDevelopment, SourcesJSON: sourcesJSON, Sources: sources, UploadedBy: &user.ID,
		}
		if err := tx.Create(&album).Error; err != nil {
			return err
		}
		if err := replaceAlbumArtistCredits(tx, album.ID, input.ArtistCredits, false, user.ID); err != nil {
			return err
		}
		if err := replaceStandaloneSongArtistCredits(tx, song.ID, input.ArtistCredits, user.ID); err != nil {
			return err
		}
		updates := map[string]any{
			"title": strings.TrimSpace(input.Title), "description": strings.TrimSpace(input.Description),
			"release_type": nil, "release_date": *releaseDate, "release_date_precision": precision,
			"album_id": album.ID, "cover_url": "", "cover_source": "", "track_number": 1, "disc_number": 1,
		}
		if err := tx.Model(&model.Song{}).Where("id = ?", song.ID).Updates(updates).Error; err != nil {
			return err
		}
		if tx.Migrator().HasTable(&model.AlbumImportSession{}) {
			if err := tx.Model(&model.AlbumImportSession{}).Where("target_song_id = ?", song.ID).
				Updates(map[string]any{"target_song_id": nil, "target_album_id": album.ID}).Error; err != nil {
				return err
			}
		}
		if _, err := revisions.EnsureInitialRevision("album", album.ID, user.ID); err != nil {
			return err
		}
		_, err := revisions.CreateCurrentSnapshotRevision("song", song.ID, user.ID, "转换为专辑曲目")
		return err
	})
	if err != nil {
		storage.DeleteMusicObjects(s.s3, []string{promoted.DestinationKey})
		return uuid.Nil, err
	}
	storage.DeleteMusicObjects(s.s3, []string{promoted.SourceKey})
	return albumID, nil
}

func (s *Service) ConvertAlbumToStandaloneSong(user authctx.CurrentUser, albumID uuid.UUID, input MusicReleaseConversionRequest) (uuid.UUID, error) {
	if user.ID == uuid.Nil {
		return uuid.Nil, apperr.Unauthorized("Login required")
	}
	releaseType := strings.ToLower(strings.TrimSpace(input.ReleaseType))
	if releaseType != "single" && releaseType != "leak" {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", "release_type must be single or leak")
	}
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.CoverURL) == "" || strings.TrimSpace(input.ReleaseDate) == "" {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", "title, cover and release_date are required")
	}
	sources, sourcesJSON, err := normalizeMusicSources(input.Sources, "")
	if err != nil {
		return uuid.Nil, err
	}
	if len(sources) == 0 {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", "standalone songs require at least one source")
	}
	releaseDate, precision, err := parseOptionalReleaseDate(input.ReleaseDate)
	if err != nil {
		return uuid.Nil, err
	}
	if releaseDate == nil {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", "release_date is required")
	}

	var songID uuid.UUID
	var promoted storage.PromotedMusicAsset
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var album model.Album
		if err := tx.First(&album, "id = ?", albumID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.album_not_found", "Album not found")
			}
			return err
		}
		if err := validateReleaseConversionProtection(tx, "album", album.ID, user.Role); err != nil {
			return err
		}
		if err := revisionservice.ValidateMusicEntryEdit(tx, "album", album.ID, user.ID); err != nil {
			return err
		}
		var songs []model.Song
		if err := tx.Where("album_id = ? AND lifecycle_status = ? AND COALESCE(status, 'open') <> ?", album.ID, model.MusicLifecycleActive, "closed").
			Order("disc_number ASC, track_number ASC, created_at ASC").Find(&songs).Error; err != nil {
			return err
		}
		if len(songs) != 1 {
			return apperr.BadRequest("validation.invalid_request", "single and leak types require exactly one song")
		}
		song := songs[0]
		songID = song.ID
		promoted, err = storage.PromoteMusicUploadAsset(
			s.s3, input.CoverURL,
			storage.BuildMusicSongCoverVersionKey(song.ID.String(), uuid.NewString(), path.Ext(input.CoverURL)),
		)
		if err != nil {
			return err
		}
		revisions := revisionservice.NewRevisionService(tx)
		if _, err := revisions.EnsureInitialRevision("song", song.ID, user.ID); err != nil {
			return err
		}
		if err := replaceStandaloneSongArtistCredits(tx, song.ID, input.ArtistCredits, user.ID); err != nil {
			return err
		}
		updates := map[string]any{
			"title": strings.TrimSpace(input.Title), "description": strings.TrimSpace(input.Description),
			"release_type": releaseType, "release_date": *releaseDate, "release_date_precision": precision,
			"sources_json": sourcesJSON, "album_id": nil, "cover_url": promoted.URL,
			"cover_source": coverSourceFromURL(promoted.URL), "track_number": 1, "disc_number": 1,
		}
		if err := tx.Model(&model.Song{}).Where("id = ?", song.ID).Updates(updates).Error; err != nil {
			return err
		}
		if tx.Migrator().HasTable(&model.AlbumImportSession{}) {
			if err := tx.Model(&model.AlbumImportSession{}).Where("target_album_id = ?", album.ID).
				Updates(map[string]any{"target_album_id": nil, "target_song_id": song.ID}).Error; err != nil {
				return err
			}
		}
		if err := removeStandaloneAlbumWrapper(tx, album.ID); err != nil {
			return err
		}
		_, err := revisions.CreateCurrentSnapshotRevision("song", song.ID, user.ID, "转换为独立歌曲")
		return err
	})
	if err != nil {
		storage.DeleteMusicObjects(s.s3, []string{promoted.DestinationKey})
		return uuid.Nil, err
	}
	storage.DeleteMusicObjects(s.s3, []string{promoted.SourceKey})
	return songID, nil
}

func validateReleaseConversionProtection(tx *gorm.DB, entityType string, entityID uuid.UUID, role string) error {
	if !tx.Migrator().HasTable(&model.ContentProtection{}) {
		return nil
	}
	var protection model.ContentProtection
	err := tx.Where("content_type = ? AND content_id = ?", entityType, entityID).First(&protection).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if protection.ProtectionLevel == "full" && !authctx.RoleAtLeast(role, authctx.RoleAdmin) {
		return apperr.Forbidden("music.edit_forbidden", "This music entry is fully protected")
	}
	return nil
}

func removeStandaloneAlbumWrapper(tx *gorm.DB, albumID uuid.UUID) error {
	if tx.Migrator().HasTable(&model.EditConflict{}) {
		if err := tx.Unscoped().Where("content_type = ? AND content_id = ?", "album", albumID).Delete(&model.EditConflict{}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&model.Revision{}) {
		if err := tx.Model(&model.Revision{}).Where("content_type = ? AND content_id = ?", "album", albumID).Update("previous_revision_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("content_type = ? AND content_id = ?", "album", albumID).Delete(&model.Revision{}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&model.ContentProtection{}) {
		if err := tx.Unscoped().Where("content_type = ? AND content_id = ?", "album", albumID).Delete(&model.ContentProtection{}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&model.AlbumBookmark{}) {
		if err := tx.Unscoped().Where("album_id = ?", albumID).Delete(&model.AlbumBookmark{}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&model.AlbumCorrection{}) {
		if err := tx.Unscoped().Where("album_id = ?", albumID).Delete(&model.AlbumCorrection{}).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("album_id = ?", albumID).Delete(&model.AlbumArtist{}).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.Album{}).Where("canonical_album_id = ?", albumID).Update("canonical_album_id", nil).Error; err != nil {
		return err
	}
	return tx.Unscoped().Delete(&model.Album{}, "id = ?", albumID).Error
}
