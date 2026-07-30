package music

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) ReplaceAlbumImportFile(user authctx.CurrentUser, sessionID, fileID uuid.UUID, input AlbumImportFileInput) (AlbumImportFileDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportFileDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return AlbumImportFileDTO{}, err
	}
	session, existing, err := loadAlbumImportFileForUser(s.db, user.ID, sessionID, fileID)
	if err != nil {
		return AlbumImportFileDTO{}, err
	}
	if session.Status != AlbumImportStatusPendingUpload && session.Status != AlbumImportStatusUploading && session.Status != AlbumImportStatusFailed && session.Status != AlbumImportStatusNeedsAttention {
		return AlbumImportFileDTO{}, apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be replaced")
	}
	if existing.UploadStatus == AlbumImportFileUploadStatusCompleting {
		return AlbumImportFileDTO{}, apperr.Unprocessable("music.import_invalid_status", "Album import file is completing upload")
	}
	oldFile := existing
	descriptors := make([]AlbumImportFileInput, 0, len(session.Files))
	for _, file := range session.Files {
		if file.ID == fileID {
			descriptors = append(descriptors, input)
			continue
		}
		descriptors = append(descriptors, AlbumImportFileInput{RelativePath: file.RelativePath, FileName: file.FileName, FileSize: file.Size, ContentType: file.ContentType})
	}
	normalized, _, err := normalizeAlbumImportFiles(descriptors, albumImportUploadLimitsFromEnv())
	if err != nil {
		return AlbumImportFileDTO{}, err
	}
	var replacement normalizedAlbumImportFile
	for index, file := range session.Files {
		if file.ID == fileID {
			replacement = normalized[index]
			break
		}
	}
	newSourceKey := albumImportSourceKey(user.ID, sessionID, fileID, replacement.Format)
	contentType := replacement.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	newUploadID, err := s.albumImportMultipart.CreateMultipartUpload(newSourceKey, contentType)
	if err != nil {
		return AlbumImportFileDTO{}, err
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		lockedSession, file, err := loadAlbumImportFileForUser(tx, user.ID, sessionID, fileID)
		if err != nil {
			return err
		}
		if lockedSession.Status != AlbumImportStatusPendingUpload && lockedSession.Status != AlbumImportStatusUploading && lockedSession.Status != AlbumImportStatusFailed && lockedSession.Status != AlbumImportStatusNeedsAttention {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be replaced")
		}
		if file.UploadStatus == AlbumImportFileUploadStatusCompleting {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file is completing upload")
		}
		lockedDescriptors := make([]AlbumImportFileInput, 0, len(lockedSession.Files))
		for _, current := range lockedSession.Files {
			if current.ID == fileID {
				lockedDescriptors = append(lockedDescriptors, input)
			} else {
				lockedDescriptors = append(lockedDescriptors, AlbumImportFileInput{RelativePath: current.RelativePath, FileName: current.FileName, FileSize: current.Size, ContentType: current.ContentType})
			}
		}
		_, lockedInputMode, err := normalizeAlbumImportFiles(lockedDescriptors, albumImportUploadLimitsFromEnv())
		if err != nil {
			return err
		}
		oldFile = file
		file.RelativePath = replacement.RelativePath
		file.FileName = replacement.FileName
		file.Role = replacement.Role
		file.DetectedFormat = replacement.Format
		file.ContentType = replacement.ContentType
		file.Size = replacement.FileSize
		file.SourceKey = newSourceKey
		file.UploadID = newUploadID
		file.PartSize = albumImportMultipartPartSize
		file.CompletedPartsJSON = "[]"
		file.UploadStatus = AlbumImportFileUploadStatusUploading
		file.ProcessingStatus = AlbumImportFileProcessingStatusPending
		file.PlaybackKey = ""
		file.ErrorMessage = ""
		if err := tx.Save(&file).Error; err != nil {
			return err
		}
		lockedSession.InputMode = lockedInputMode
		lockedSession.Status = AlbumImportStatusUploading
		lockedSession.Stage = AlbumImportStageUpload
		lockedSession.ErrorMessage = ""
		if err := refreshAlbumImportUploadProgress(tx, &lockedSession); err != nil {
			return err
		}
		existing = file
		return nil
	})
	if err != nil {
		s.abortAlbumImportMultipartOrRecord(sessionID, model.AlbumImportFile{SourceKey: newSourceKey, UploadID: newUploadID})
		return AlbumImportFileDTO{}, err
	}
	s.cleanupAlbumImportObject(oldFile)
	return buildAlbumImportFileDTO(existing), nil
}
