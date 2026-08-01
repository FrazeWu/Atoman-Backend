package music

import (
	"encoding/json"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	maxAlbumImportArchiveSize          = defaultAlbumImportMaxFileBytes
	albumImportMultipartPartSize int64 = 16 * 1024 * 1024
)

func buildAlbumImportDTO(session model.AlbumImportSession) AlbumImportDTO {
	payload := map[string]any{}
	if strings.TrimSpace(session.PayloadJSON) != "" {
		_ = json.Unmarshal([]byte(session.PayloadJSON), &payload)
	}

	inputMode := session.InputMode
	if inputMode == "" {
		inputMode = AlbumImportInputModeAuto
	}
	stage := session.Stage
	if stage == "" {
		stage = AlbumImportStageUpload
	}
	errorMessage := session.ErrorMessage
	if errorMessage == "" {
		errorMessage = stringValue(payload["error_message"])
	}

	coverURL := resolveAlbumImportCoverURL(payload)
	dto := AlbumImportDTO{
		ImportID:  session.ID.String(),
		Status:    session.Status,
		InputMode: inputMode,
		Stage:     stage,
		Progress: AlbumImportProgressDTO{
			Current: session.ProgressCurrent,
			Total:   session.ProgressTotal,
		},
		Files:             []AlbumImportFileDTO{},
		Tracks:            []AlbumImportDTOTrack{},
		Errors:            []AlbumImportErrorDTO{},
		ArchiveName:       stringValue(payload["archive_name"]),
		UploadProgress:    floatValue(payload["upload_progress"]),
		UploadSpeed:       floatValue(payload["upload_speed"]),
		CoverURL:          coverURL,
		CoverKey:          stringValue(payload["cover_key"]),
		DerivedAlbumTitle: stringValue(payload["derived_album_title"]),
		DerivedCover:      stringValue(payload["derived_cover"]),
		LastSyncedAt:      session.UpdatedAt.Format(time.RFC3339),
		ErrorMessage:      errorMessage,
		DerivedTracks:     []AlbumImportDTOTrack{},
	}
	for _, file := range session.Files {
		fileDTO := buildAlbumImportFileDTO(file)
		dto.Files = append(dto.Files, fileDTO)
		if file.ErrorMessage != "" {
			dto.Errors = append(dto.Errors, AlbumImportErrorDTO{
				FileID:  file.ID.String(),
				Message: file.ErrorMessage,
			})
		}
	}

	if rawTracks, ok := payload["derived_tracks"].([]any); ok {
		for _, rawTrack := range rawTracks {
			trackMap, ok := rawTrack.(map[string]any)
			if !ok {
				continue
			}
			track := AlbumImportDTOTrack{
				Title:    stringValue(trackMap["title"]),
				AudioKey: stringValue(trackMap["audio_key"]),
				AudioURL: stringValue(trackMap["audio_url"]),
				Origin:   stringValue(trackMap["origin"]),
			}
			dto.DerivedTracks = append(dto.DerivedTracks, track)
			dto.Tracks = append(dto.Tracks, track)
		}
	}
	if dto.ErrorMessage != "" {
		dto.Errors = append(dto.Errors, AlbumImportErrorDTO{Message: dto.ErrorMessage})
	}

	return dto
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func floatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

func (s *Service) CreateAlbumImportSession(user authctx.CurrentUser, input CreateAlbumImportSessionInput) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}

	status := normalizeAlbumImportStatus(input.Status)
	if !isAlbumImportStatusAllowed(status) {
		return model.AlbumImportSession{}, apperr.BadRequest("validation.invalid_request", "invalid import status")
	}

	payloadJSON, err := json.Marshal(input.Payload)
	if err != nil {
		return model.AlbumImportSession{}, apperr.BadRequest("validation.invalid_request", "payload is not valid")
	}

	session := model.AlbumImportSession{
		UserID:      &user.ID,
		InputMode:   AlbumImportInputModeAuto,
		PayloadJSON: string(payloadJSON),
	}
	payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	applyAlbumImportSessionState(&session, status, payload)
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	session.ExpiresAt = &expiresAt
	if err := s.db.Create(&session).Error; err != nil {
		return model.AlbumImportSession{}, err
	}
	return session, nil
}

func (s *Service) deleteAlbumImportSessionObjectOrRecord(sessionID uuid.UUID, key string) error {
	if err := s.albumImportMultipart.DeleteObject(key); err != nil {
		return recordAlbumImportSessionCleanupTarget(s.db, sessionID, albumImportCleanupTarget{Action: "delete", Key: key})
	}
	return nil
}

func (s *Service) updateAlbumImportStatusAndPayload(id uuid.UUID, status string, payload map[string]any) (model.AlbumImportSession, error) {
	var session model.AlbumImportSession
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&session, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.import_not_found", "Import session not found")
			}
			return err
		}
		applyAlbumImportSessionState(&session, status, payload)
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		session.PayloadJSON = string(payloadJSON)
		return tx.Save(&session).Error
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	return session, nil
}

func applyAlbumImportSessionState(session *model.AlbumImportSession, status string, payload map[string]any) {
	session.Status = status
	if status == AlbumImportStatusFailed {
		session.Stage = AlbumImportStageFailed
		session.ErrorMessage = strings.TrimSpace(stringValue(payload["error_message"]))
		expiresAt := albumImportFailureExpiresAt()
		session.ExpiresAt = &expiresAt
		return
	}

	delete(payload, "error_message")
	session.ErrorMessage = ""
	switch status {
	case AlbumImportStatusPendingUpload:
		session.Stage = AlbumImportStageUpload
		session.ProgressCurrent = 0
		session.ProgressTotal = 0
	case AlbumImportStatusUploading:
		session.Stage = AlbumImportStageUpload
		session.ProgressCurrent = albumImportCompletedPartBytes(payload["multipart_completed_parts"])
		session.ProgressTotal = int64Value(payload["multipart_file_size"])
	case AlbumImportStatusUploaded:
		session.Stage = AlbumImportStageUpload
		session.ProgressTotal = int64Value(payload["multipart_file_size"])
		session.ProgressCurrent = session.ProgressTotal
	case AlbumImportStatusQueued:
		session.Stage = AlbumImportStageQueued
		session.ProgressCurrent = 0
		session.ProgressTotal = 0
	case AlbumImportStatusExtracting:
		session.Stage = AlbumImportStageExtracting
		session.ProgressCurrent = 0
		session.ProgressTotal = 0
	case AlbumImportStatusAnalyzing:
		session.Stage = AlbumImportStageAnalyzing
		session.ProgressCurrent = 0
		session.ProgressTotal = 0
	case AlbumImportStatusTranscoding:
		session.Stage = AlbumImportStageTranscoding
		session.ProgressCurrent = 0
		session.ProgressTotal = 0
	case AlbumImportStatusReady, AlbumImportStatusNeedsAttention:
		session.Stage = AlbumImportStageReady
		trackCount := albumImportDerivedTrackCount(payload["derived_tracks"])
		session.ProgressCurrent = trackCount
		session.ProgressTotal = trackCount
	case AlbumImportStatusCommitting:
		session.Stage = AlbumImportStageCommitting
	case AlbumImportStatusCommitted:
		session.Stage = AlbumImportStageCompleted
		if session.ProgressTotal < 1 {
			session.ProgressTotal = 1
		}
		session.ProgressCurrent = session.ProgressTotal
	case AlbumImportStatusCanceled:
		session.Stage = AlbumImportStageCanceled
	}
}

func albumImportFailureExpiresAt() time.Time { return time.Now().UTC().Add(7 * 24 * time.Hour) }

func (s *Service) markAlbumImportFailed(id uuid.UUID, message string) error {
	session, err := s.GetAlbumImportSession(id)
	if err != nil {
		return err
	}
	payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		return err
	}
	payload["error_message"] = strings.TrimSpace(message)
	_, err = s.updateAlbumImportStatusAndPayload(id, AlbumImportStatusFailed, payload)
	return err
}

func (s *Service) GetAlbumImportSession(id uuid.UUID) (model.AlbumImportSession, error) {
	return loadAlbumImportSession(s.db, id, nil)
}

func (s *Service) GetAlbumImportSessionForUser(user authctx.CurrentUser, id uuid.UUID) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	return loadAlbumImportSession(s.db, id, &user.ID)
}

func loadAlbumImportSession(db *gorm.DB, id uuid.UUID, userID *uuid.UUID) (model.AlbumImportSession, error) {
	var session model.AlbumImportSession
	query := db.Preload("Files").Preload("Job")
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
	}
	if err := query.First(&session, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.AlbumImportSession{}, apperr.NotFound("music.import_not_found", "Import session not found")
		}
		return model.AlbumImportSession{}, err
	}
	return session, nil
}

func normalizeAlbumImportStatus(status string) string {
	if strings.TrimSpace(status) == "" {
		return AlbumImportStatusPendingUpload
	}
	return strings.TrimSpace(strings.ToLower(status))
}

func isAlbumImportStatusAllowed(status string) bool {
	switch status {
	case AlbumImportStatusPendingUpload, AlbumImportStatusReady, AlbumImportStatusCommitted:
		return true
	default:
		return false
	}
}

func isAlbumImportActiveStatus(status string) bool {
	switch status {
	case AlbumImportStatusUploading, AlbumImportStatusQueued, AlbumImportStatusExtracting, AlbumImportStatusAnalyzing, AlbumImportStatusTranscoding:
		return true
	default:
		return false
	}
}

func isAlbumImportMultipartStartStatus(status string) bool {
	switch status {
	case AlbumImportStatusPendingUpload, AlbumImportStatusUploading, AlbumImportStatusFailed:
		return true
	default:
		return false
	}
}

func isAlbumImportMultipartPartUploadStatus(status string) bool {
	switch status {
	case AlbumImportStatusPendingUpload, AlbumImportStatusUploading:
		return true
	default:
		return false
	}
}

func readAlbumImportPayloadMap(payloadJSON string) (map[string]any, error) {
	payload := map[string]any{}
	if strings.TrimSpace(payloadJSON) == "" {
		return payload, nil
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", "payload is not valid JSON")
	}
	return payload, nil
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	default:
		return 0
	}
}

func mustMarshalStageNames(values []ArtistStageNamePayload) string {
	filtered := make([]ArtistStageNamePayload, 0, len(values))
	for _, value := range values {
		value.Name = strings.TrimSpace(value.Name)
		value.StartDateText = strings.TrimSpace(value.StartDateText)
		value.EndDateText = strings.TrimSpace(value.EndDateText)
		if value.Name == "" {
			continue
		}
		filtered = append(filtered, value)
	}
	raw, err := json.Marshal(filtered)
	if err != nil {
		return "[]"
	}
	return string(raw)
}
