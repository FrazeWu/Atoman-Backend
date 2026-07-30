package music

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) RegisterAlbumImportFiles(user authctx.CurrentUser, id uuid.UUID, input RegisterAlbumImportFilesInput) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return model.AlbumImportSession{}, err
	}
	files, inputMode, err := normalizeAlbumImportFiles(input.Files, albumImportUploadLimitsFromEnv())
	if err != nil {
		return model.AlbumImportSession{}, err
	}

	createdUploads := make([]model.AlbumImportFile, 0, len(files))
	var out model.AlbumImportSession
	err = s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, id, user.ID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusPendingUpload || len(session.Files) != 0 {
			return apperr.Unprocessable("music.import_invalid_status", "Import session cannot register files")
		}

		for _, descriptor := range files {
			file := model.AlbumImportFile{
				ImportID:           session.ID,
				RelativePath:       descriptor.RelativePath,
				FileName:           descriptor.FileName,
				Role:               descriptor.Role,
				DetectedFormat:     descriptor.Format,
				ContentType:        descriptor.ContentType,
				Size:               descriptor.FileSize,
				PartSize:           albumImportMultipartPartSize,
				CompletedPartsJSON: "[]",
				CleanupJSON:        "[]",
				UploadStatus:       AlbumImportFileUploadStatusUploading,
				ProcessingStatus:   AlbumImportFileProcessingStatusPending,
				MetadataJSON:       "{}",
			}
			file.ID = uuid.New()
			file.SourceKey = albumImportSourceKey(user.ID, session.ID, file.ID, descriptor.Format)
			if err := tx.Create(&file).Error; err != nil {
				return err
			}
			createdUploads = append(createdUploads, file)
		}
		session.InputMode = inputMode
		session.Stage = AlbumImportStageUpload
		session.ProgressCurrent = 0
		session.ProgressTotal = 0
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = session
		out.Files = createdUploads
		return nil
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	for index := range createdUploads {
		contentType := createdUploads[index].ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		uploadID, createErr := s.albumImportMultipart.CreateMultipartUpload(createdUploads[index].SourceKey, contentType)
		if createErr != nil {
			for _, file := range createdUploads[:index] {
				s.abortAlbumImportMultipartOrRecord(id, file)
			}
			if err := s.failRegisteredAlbumImportFiles(user.ID, id, createdUploads, createErr.Error()); err != nil {
				return model.AlbumImportSession{}, err
			}
			return model.AlbumImportSession{}, createErr
		}
		createdUploads[index].UploadID = uploadID
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, id, user.ID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusPendingUpload {
			return apperr.Unprocessable("music.import_invalid_status", "Import session cannot register files")
		}
		var totalSize int64
		for _, file := range createdUploads {
			result := tx.Model(&model.AlbumImportFile{}).Where("id = ? AND import_id = ? AND upload_id = ?", file.ID, id, "").Update("upload_id", file.UploadID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return apperr.Unprocessable("music.import_invalid_status", "Import session cannot register files")
			}
			totalSize += file.Size
		}
		session.Status = AlbumImportStatusUploading
		session.Stage = AlbumImportStageUpload
		session.ProgressCurrent = 0
		session.ProgressTotal = totalSize
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = session
		out.Files = createdUploads
		return nil
	})
	if err != nil {
		for _, file := range createdUploads {
			s.abortAlbumImportMultipartOrRecord(id, file)
		}
		return model.AlbumImportSession{}, err
	}
	return out, nil
}

func (s *Service) failRegisteredAlbumImportFiles(userID, sessionID uuid.UUID, files []model.AlbumImportFile, message string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, sessionID, userID)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.AlbumImportFile{}).Where("import_id = ?", sessionID).Updates(map[string]any{"upload_status": AlbumImportFileUploadStatusFailed, "error_message": message}).Error; err != nil {
			return err
		}
		session.Status, session.Stage, session.ErrorMessage = AlbumImportStatusFailed, AlbumImportStageFailed, message
		expiresAt := albumImportFailureExpiresAt()
		session.ExpiresAt = &expiresAt
		return tx.Save(&session).Error
	})
}
