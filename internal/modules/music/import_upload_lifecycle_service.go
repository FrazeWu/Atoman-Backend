package music

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) CompleteAlbumImportSession(user authctx.CurrentUser, sessionID uuid.UUID) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, sessionID, user.ID)
		if err != nil {
			return err
		}
		if session.Status == AlbumImportStatusCommitted {
			return nil
		}
		if session.Status == AlbumImportStatusCanceled {
			return apperr.Unprocessable("music.import_invalid_status", "Import session cannot be queued")
		}
		if session.Status == AlbumImportStatusQueued {
			return nil
		}
		if session.Status != AlbumImportStatusUploading {
			return apperr.Unprocessable("music.import_invalid_status", "Import session cannot be queued")
		}
		if len(session.Files) == 0 {
			return apperr.Unprocessable("music.import_invalid_status", "Album import has no files")
		}
		hasImportSource := false
		for _, file := range session.Files {
			if file.UploadStatus != AlbumImportFileUploadStatusUploaded {
				return apperr.Unprocessable("music.import_invalid_status", "Album import uploads are incomplete")
			}
			hasImportSource = hasImportSource || file.Role == AlbumImportFileRoleArchive || file.Role == AlbumImportFileRoleAudio
		}
		if !hasImportSource {
			return apperr.BadRequest("validation.invalid_request", "Album import requires an archive or audio file")
		}
		if err := queueAlbumImportSession(tx, &session, true); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	session, err := loadAlbumImportSession(s.db, sessionID, &user.ID)
	if err == nil {
		s.updateAlbumImportNotification(session)
	}
	return session, err
}

// RepairAlbumImportSession reopens one committed session so its original album
// can be updated. Normal duplicate commit requests remain idempotent.
func (s *Service) RepairAlbumImportSession(user authctx.CurrentUser, sessionID uuid.UUID) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, sessionID, user.ID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusCommitted || (session.TargetAlbumID == nil && session.TargetSongID == nil) {
			return apperr.Unprocessable("music.import_invalid_status", "Committed import with a target entry is required")
		}
		payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
		if err != nil {
			return err
		}
		currentTracks := []map[string]any{}
		if session.TargetSongID != nil {
			var song model.Song
			if err := tx.First(&song, "id = ?", *session.TargetSongID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return apperr.NotFound("music.song_not_found", "Target song not found")
				}
				return err
			}
			if song.AlbumID != nil || song.ReleaseType == nil {
				return apperr.Unprocessable("music.import_invalid_status", "Target song is not standalone")
			}
			payload["cover_url"] = song.CoverURL
			currentTracks = append(currentTracks, map[string]any{
				"song_id": song.ID.String(), "title": song.Title, "disc_number": 1, "track_number": 1,
				"audio_url": song.AudioURL,
			})
		} else {
			var album model.Album
			if err := tx.First(&album, "id = ?", *session.TargetAlbumID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return apperr.NotFound("music.album_not_found", "Target album not found")
				}
				return err
			}
			var songs []model.Song
			if err := tx.Where("album_id = ? AND lifecycle_status = ?", album.ID, model.MusicLifecycleActive).Order("disc_number ASC, track_number ASC, created_at ASC").Find(&songs).Error; err != nil {
				return err
			}
			for _, song := range songs {
				currentTracks = append(currentTracks, map[string]any{
					"song_id": song.ID.String(), "title": song.Title, "disc_number": song.DiscNumber, "track_number": song.TrackNumber,
					"audio_url": song.AudioURL,
				})
			}
			payload["cover_url"] = album.CoverURL
		}
		delete(payload, "cover_key")
		delete(payload, "derived_cover")
		payload["derived_tracks"] = currentTracks
		applyAlbumImportSessionState(&session, AlbumImportStatusReady, payload)
		session.CommittedAt = nil
		session.CommittedBy = nil
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		session.PayloadJSON = string(encoded)
		return tx.Save(&session).Error
	}); err != nil {
		return model.AlbumImportSession{}, err
	}
	session, err := loadAlbumImportSession(s.db, sessionID, &user.ID)
	if err == nil {
		s.updateAlbumImportNotification(session)
	}
	return session, err
}

func (s *Service) CancelAlbumImportSession(user authctx.CurrentUser, sessionID uuid.UUID) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	var files []model.AlbumImportFile
	err := s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, sessionID, user.ID)
		if err != nil {
			return err
		}
		if session.Status == AlbumImportStatusCommitted {
			return apperr.Unprocessable("music.import_invalid_status", "Committed import cannot be canceled")
		}
		if session.Status == AlbumImportStatusCanceled {
			files = nil
			return nil
		}
		for _, file := range session.Files {
			if file.UploadStatus == AlbumImportFileUploadStatusCompleting {
				return apperr.Unprocessable("music.import_invalid_status", "Import session has a file completing upload")
			}
		}
		for _, file := range session.Files {
			if file.UploadStatus != AlbumImportFileUploadStatusUploaded && file.SourceKey != "" && file.UploadID != "" {
				if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
					return err
				}
				break
			}
		}
		files = append([]model.AlbumImportFile{}, session.Files...)
		expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
		if err := tx.Model(&model.AlbumImportSession{}).Where("id = ? AND user_id = ?", sessionID, user.ID).Updates(map[string]any{
			"status": AlbumImportStatusCanceled, "stage": AlbumImportStageCanceled, "error_message": "", "expires_at": expiresAt,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.AlbumImportJob{}).Where("import_id = ?", sessionID).Updates(map[string]any{
			"status": AlbumImportJobStatusCanceled, "stage": AlbumImportStageCanceled,
			"locked_by": "", "locked_at": nil, "heartbeat_at": nil, "finished_at": nil,
		}).Error
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	if s.albumImportMultipart != nil {
		for _, file := range files {
			if file.UploadStatus != AlbumImportFileUploadStatusUploaded && file.SourceKey != "" && file.UploadID != "" {
				s.cleanupAlbumImportObject(file)
			}
		}
	}
	session, err := loadAlbumImportSession(s.db, sessionID, &user.ID)
	if err == nil {
		s.updateAlbumImportNotification(session)
	}
	return session, err
}

func (s *Service) DeleteAlbumImportFile(user authctx.CurrentUser, sessionID, fileID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	var file model.AlbumImportFile
	err := s.db.Transaction(func(tx *gorm.DB) error {
		session, lockedFile, err := loadAlbumImportFileForUser(tx, user.ID, sessionID, fileID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusPendingUpload && session.Status != AlbumImportStatusUploading {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be deleted after processing starts")
		}
		if lockedFile.UploadStatus == AlbumImportFileUploadStatusCompleting {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file is completing upload")
		}
		file = lockedFile
		if err := tx.Delete(&model.AlbumImportFile{}, "id = ? AND import_id = ?", fileID, sessionID).Error; err != nil {
			return err
		}
		return refreshAlbumImportFileSet(tx, &session)
	})
	if err != nil {
		return err
	}
	s.cleanupAlbumImportObject(file)
	return nil
}

func (s *Service) RetryAlbumImportFile(user authctx.CurrentUser, sessionID, fileID uuid.UUID) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	session, file, err := loadAlbumImportFileForUser(s.db, user.ID, sessionID, fileID)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	if file.UploadStatus == AlbumImportFileUploadStatusFailed {
		return s.retryAlbumImportFileUpload(user, session, file)
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		session, file, err := loadAlbumImportFileForUser(tx, user.ID, sessionID, fileID)
		if err != nil {
			return err
		}
		hasProcessingError := file.ProcessingStatus == AlbumImportFileProcessingStatusFailed || file.ErrorMessage != "" || session.ErrorMessage != ""
		if (session.Status != AlbumImportStatusFailed && session.Status != AlbumImportStatusNeedsAttention) || file.UploadStatus != AlbumImportFileUploadStatusUploaded || !hasProcessingError {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be retried")
		}
		file.ProcessingStatus = AlbumImportFileProcessingStatusPending
		file.ErrorMessage = ""
		if err := tx.Save(&file).Error; err != nil {
			return err
		}
		if err := queueAlbumImportSession(tx, &session, true); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	return loadAlbumImportSession(s.db, sessionID, &user.ID)
}

func (s *Service) retryAlbumImportFileUpload(user authctx.CurrentUser, session model.AlbumImportSession, file model.AlbumImportFile) (model.AlbumImportSession, error) {
	if session.Status != AlbumImportStatusFailed && session.Status != AlbumImportStatusNeedsAttention {
		return model.AlbumImportSession{}, apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be retried")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return model.AlbumImportSession{}, err
	}
	newSourceKey := albumImportSourceKey(user.ID, session.ID, file.ID, file.DetectedFormat)
	contentType := strings.TrimSpace(file.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	newUploadID, err := s.albumImportMultipart.CreateMultipartUpload(newSourceKey, contentType)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	oldFile := file
	err = s.db.Transaction(func(tx *gorm.DB) error {
		lockedSession, lockedFile, err := loadAlbumImportFileForUser(tx, user.ID, session.ID, file.ID)
		if err != nil {
			return err
		}
		if (lockedSession.Status != AlbumImportStatusFailed && lockedSession.Status != AlbumImportStatusNeedsAttention) || lockedFile.UploadStatus != AlbumImportFileUploadStatusFailed || lockedFile.SourceKey != file.SourceKey {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be retried")
		}
		lockedFile.SourceKey = newSourceKey
		lockedFile.UploadID = newUploadID
		lockedFile.PartSize = albumImportMultipartPartSize
		lockedFile.CompletedPartsJSON = "[]"
		lockedFile.UploadStatus = AlbumImportFileUploadStatusUploading
		lockedFile.ProcessingStatus = AlbumImportFileProcessingStatusPending
		lockedFile.ErrorMessage = ""
		if err := tx.Save(&lockedFile).Error; err != nil {
			return err
		}
		payload, err := readAlbumImportPayloadMap(lockedSession.PayloadJSON)
		if err != nil {
			return err
		}
		delete(payload, "error_message")
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		lockedSession.Status = AlbumImportStatusUploading
		lockedSession.Stage = AlbumImportStageUpload
		lockedSession.ErrorMessage = ""
		lockedSession.PayloadJSON = string(payloadJSON)
		return refreshAlbumImportUploadProgress(tx, &lockedSession)
	})
	if err != nil {
		s.abortAlbumImportMultipartOrRecord(session.ID, model.AlbumImportFile{SourceKey: newSourceKey, UploadID: newUploadID})
		return model.AlbumImportSession{}, err
	}
	s.cleanupAlbumImportObject(oldFile)
	return loadAlbumImportSession(s.db, session.ID, &user.ID)
}
