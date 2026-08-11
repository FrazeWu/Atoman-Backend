package music

import (
	"encoding/json"
	"sort"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func loadAlbumImportFileForUser(db *gorm.DB, userID, sessionID, fileID uuid.UUID) (model.AlbumImportSession, model.AlbumImportFile, error) {
	session, err := loadAlbumImportSessionForUpdate(db, sessionID, userID)
	if err != nil {
		return model.AlbumImportSession{}, model.AlbumImportFile{}, err
	}
	var file model.AlbumImportFile
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&file, "id = ? AND import_id = ?", fileID, sessionID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.AlbumImportSession{}, model.AlbumImportFile{}, apperr.NotFound("music.import_file_not_found", "Album import file not found")
		}
		return model.AlbumImportSession{}, model.AlbumImportFile{}, err
	}
	return session, file, nil
}

func loadAlbumImportSessionForUpdate(db *gorm.DB, sessionID, userID uuid.UUID) (model.AlbumImportSession, error) {
	var session model.AlbumImportSession
	err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("Files").Preload("Job").Where("user_id = ?", userID).First(&session, "id = ?", sessionID).Error
	if err == gorm.ErrRecordNotFound {
		return model.AlbumImportSession{}, apperr.NotFound("music.import_not_found", "Import session not found")
	}
	return session, err
}

func (s *Service) cleanupAlbumImportObject(file model.AlbumImportFile) error {
	if s.albumImportMultipart == nil || file.SourceKey == "" {
		return nil
	}
	target := albumImportCleanupTarget{Key: file.SourceKey}
	var err error
	if file.UploadStatus == AlbumImportFileUploadStatusUploaded {
		target.Action = "delete"
		err = s.albumImportMultipart.DeleteObject(file.SourceKey)
	} else if file.UploadID != "" {
		target.Action = "abort"
		target.UploadID = file.UploadID
		err = s.albumImportMultipart.AbortMultipartUpload(file.SourceKey, file.UploadID)
	}
	if err != nil && target.Action != "" {
		return recordAlbumImportCleanupTarget(s.db, file.ID, target)
	}
	return nil
}

func (s *Service) abortAlbumImportMultipartOrRecord(sessionID uuid.UUID, file model.AlbumImportFile) error {
	if s.albumImportMultipart == nil || file.SourceKey == "" || file.UploadID == "" {
		return nil
	}
	if err := s.albumImportMultipart.AbortMultipartUpload(file.SourceKey, file.UploadID); err != nil {
		return recordAlbumImportSessionCleanupTarget(s.db, sessionID, albumImportCleanupTarget{Action: "abort", Key: file.SourceKey, UploadID: file.UploadID})
	}
	return nil
}

func recordAlbumImportCleanupTarget(db *gorm.DB, fileID uuid.UUID, target albumImportCleanupTarget) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var file model.AlbumImportFile
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&file, "id = ?", fileID).Error; err != nil {
			return err
		}
		targets := []albumImportCleanupTarget{}
		if strings.TrimSpace(file.CleanupJSON) != "" {
			_ = json.Unmarshal([]byte(file.CleanupJSON), &targets)
		}
		targets = append(targets, target)
		encoded, err := json.Marshal(targets)
		if err != nil {
			return err
		}
		return tx.Unscoped().Model(&model.AlbumImportFile{}).Where("id = ?", fileID).Update("cleanup_json", string(encoded)).Error
	})
}

func recordAlbumImportSessionCleanupTarget(db *gorm.DB, sessionID uuid.UUID, target albumImportCleanupTarget) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var session model.AlbumImportSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, "id = ?", sessionID).Error; err != nil {
			return err
		}
		payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
		if err != nil {
			return err
		}
		targets := []albumImportCleanupTarget{}
		if rawTargets, ok := payload["cleanup_targets"]; ok {
			raw, _ := json.Marshal(rawTargets)
			_ = json.Unmarshal(raw, &targets)
		}
		targets = append(targets, target)
		payload["cleanup_targets"] = targets
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		return tx.Model(&model.AlbumImportSession{}).Where("id = ?", sessionID).Update("payload_json", string(encoded)).Error
	})
}

func albumImportFileCompletedParts(file model.AlbumImportFile) ([]AlbumImportMultipartPartDTO, error) {
	parts := []AlbumImportMultipartPartDTO{}
	if strings.TrimSpace(file.CompletedPartsJSON) == "" {
		return parts, nil
	}
	if err := json.Unmarshal([]byte(file.CompletedPartsJSON), &parts); err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", "Completed parts are invalid")
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	return parts, nil
}

func refreshAlbumImportUploadProgress(tx *gorm.DB, session *model.AlbumImportSession) error {
	var files []model.AlbumImportFile
	if err := tx.Where("import_id = ?", session.ID).Find(&files).Error; err != nil {
		return err
	}
	var current, total int64
	for _, file := range files {
		total += file.Size
		if file.UploadStatus == AlbumImportFileUploadStatusUploaded {
			current += file.Size
		}
	}
	session.ProgressCurrent = current
	session.ProgressTotal = total
	return tx.Save(session).Error
}

func refreshAlbumImportFileSet(tx *gorm.DB, session *model.AlbumImportSession) error {
	var files []model.AlbumImportFile
	if err := tx.Where("import_id = ?", session.ID).Find(&files).Error; err != nil {
		return err
	}
	if len(files) == 0 {
		session.InputMode = AlbumImportInputModeAuto
		session.Status = AlbumImportStatusPendingUpload
		session.ProgressCurrent = 0
		session.ProgressTotal = 0
		return tx.Save(session).Error
	}
	descriptors := make([]AlbumImportFileInput, 0, len(files))
	for _, file := range files {
		descriptors = append(descriptors, AlbumImportFileInput{RelativePath: file.RelativePath, FileName: file.FileName, FileSize: file.Size, ContentType: file.ContentType})
	}
	_, inputMode, err := normalizeAlbumImportFiles(descriptors, albumImportUploadLimitsFromEnv())
	if err != nil {
		return err
	}
	session.InputMode = inputMode
	return refreshAlbumImportUploadProgress(tx, session)
}

func queueSubmittedAlbumImportWhenUploadsComplete(tx *gorm.DB, session *model.AlbumImportSession) error {
	if session.Status != AlbumImportStatusUploading {
		return nil
	}
	payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		return err
	}
	if _, submitted := payload["commit_request"]; !submitted {
		return nil
	}
	var total, incomplete int64
	if err := tx.Model(&model.AlbumImportFile{}).Where("import_id = ?", session.ID).Count(&total).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.AlbumImportFile{}).Where("import_id = ? AND upload_status <> ?", session.ID, AlbumImportFileUploadStatusUploaded).Count(&incomplete).Error; err != nil {
		return err
	}
	if total == 0 || incomplete > 0 {
		return nil
	}
	return queueAlbumImportSession(tx, session, true)
}

func queueAlbumImportSession(tx *gorm.DB, session *model.AlbumImportSession, resetJob bool) error {
	payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		return err
	}
	delete(payload, "error_message")
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	session.PayloadJSON = string(payloadJSON)
	session.Status = AlbumImportStatusQueued
	session.Stage = AlbumImportStageQueued
	session.ProgressCurrent = 0
	session.ProgressTotal = 0
	session.ErrorMessage = ""
	if err := tx.Save(session).Error; err != nil {
		return err
	}
	var job model.AlbumImportJob
	err = tx.First(&job, "import_id = ?", session.ID).Error
	if err == gorm.ErrRecordNotFound {
		return tx.Create(&model.AlbumImportJob{ImportID: session.ID, Status: AlbumImportJobStatusQueued, Stage: AlbumImportStageQueued, MaxAttempts: 3}).Error
	}
	if err != nil || !resetJob {
		return err
	}
	job.Status = AlbumImportJobStatusQueued
	job.Stage = AlbumImportStageQueued
	job.Attempts = 0
	job.MaxAttempts = 3
	job.LockedBy = ""
	job.LockedAt = nil
	job.HeartbeatAt = nil
	job.StartedAt = nil
	job.FinishedAt = nil
	job.LastError = ""
	return tx.Save(&job).Error
}

func buildAlbumImportFileDTO(file model.AlbumImportFile) AlbumImportFileDTO {
	parts := []AlbumImportMultipartPartDTO{}
	if strings.TrimSpace(file.CompletedPartsJSON) != "" {
		_ = json.Unmarshal([]byte(file.CompletedPartsJSON), &parts)
	}
	if parts == nil {
		parts = []AlbumImportMultipartPartDTO{}
	}
	return AlbumImportFileDTO{
		ID: file.ID.String(), RelativePath: file.RelativePath, FileName: file.FileName,
		Role: file.Role, DetectedFormat: file.DetectedFormat, ContentType: file.ContentType,
		Size: file.Size, SourceKey: file.SourceKey, PartSize: file.PartSize, CompletedParts: parts,
		PlaybackKey: file.PlaybackKey, UploadStatus: file.UploadStatus,
		ProcessingStatus: file.ProcessingStatus, DiscNumber: file.DiscNumber,
		TrackNumber: file.TrackNumber, Title: file.Title, DurationSeconds: file.DurationSeconds,
		ErrorMessage: file.ErrorMessage,
	}
}

func saveLegacyAlbumImportFile(tx *gorm.DB, file *model.AlbumImportFile, sessionID uuid.UUID, state albumImportMultipartState, contentType string) error {
	if file.ID == uuid.Nil {
		file.ID = uuid.New()
	}
	encodedParts, err := json.Marshal(state.CompletedParts)
	if err != nil {
		return err
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/zip"
	}
	file.ImportID = sessionID
	file.RelativePath = state.FileName
	file.FileName = state.FileName
	file.Role = AlbumImportFileRoleArchive
	file.DetectedFormat = "zip"
	file.ContentType = contentType
	file.Size = state.FileSize
	file.SourceKey = state.ObjectKey
	file.UploadID = state.UploadID
	file.PartSize = state.PartSize
	file.CompletedPartsJSON = string(encodedParts)
	file.UploadStatus = AlbumImportFileUploadStatusUploading
	if file.ProcessingStatus == "" {
		file.ProcessingStatus = AlbumImportFileProcessingStatusPending
	}
	if file.MetadataJSON == "" {
		file.MetadataJSON = "{}"
	}
	if file.CleanupJSON == "" {
		file.CleanupJSON = "[]"
	}
	return tx.Save(file).Error
}
