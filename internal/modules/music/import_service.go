package music

import (
	"encoding/json"
	"path/filepath"
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
	albumTitle := albumImportSessionAlbumTitle(session, payload)
	missingArtists := []string{}
	if values, ok := payload["missing_artists"].([]any); ok {
		for _, value := range values {
			if item := strings.TrimSpace(stringValue(value)); item != "" {
				missingArtists = append(missingArtists, item)
			}
		}
	}
	dto := AlbumImportDTO{
		ImportID: session.ID.String(),
		TargetAlbumID: func() string {
			if session.TargetAlbumID == nil {
				return ""
			}
			return session.TargetAlbumID.String()
		}(),
		ArtistID:   stringValue(payload["artist_id"]),
		AlbumTitle: albumTitle,
		Status:     session.Status,
		InputMode:  inputMode,
		Stage:      stage,
		Progress: AlbumImportProgressDTO{
			Current: session.ProgressCurrent,
			Total:   session.ProgressTotal,
		},
		Files:              []AlbumImportFileDTO{},
		Tracks:             []AlbumImportDTOTrack{},
		Errors:             []AlbumImportErrorDTO{},
		ArchiveName:        stringValue(payload["archive_name"]),
		UploadProgress:     floatValue(payload["upload_progress"]),
		UploadSpeed:        floatValue(payload["upload_speed"]),
		CoverURL:           coverURL,
		CoverKey:           stringValue(payload["cover_key"]),
		DerivedAlbumTitle:  stringValue(payload["derived_album_title"]),
		DerivedCover:       stringValue(payload["derived_cover"]),
		DerivedReleaseDate: stringValue(payload["derived_release_date"]),
		DerivedAlbumType:   stringValue(payload["derived_album_type"]),
		MetadataSourceURL:  stringValue(payload["metadata_source_url"]),
		MissingArtists:     missingArtists,
		LastSyncedAt:       session.UpdatedAt.Format(time.RFC3339),
		ErrorMessage:       errorMessage,
		DerivedTracks:      []AlbumImportDTOTrack{},
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
				SongID:       stringValue(trackMap["song_id"]),
				Title:        stringValue(trackMap["title"]),
				AudioKey:     stringValue(trackMap["audio_key"]),
				AudioURL:     stringValue(trackMap["audio_url"]),
				Origin:       stringValue(trackMap["origin"]),
				DiscNumber:   int(int64Value(trackMap["disc_number"])),
				TrackNumber:  int(int64Value(trackMap["track_number"])),
				LyricsSource: stringValue(trackMap["lyrics_source"]),
			}
			if lyricsMap, ok := trackMap["lyrics"].(map[string]any); ok {
				track.Lyrics = &AlbumImportTrackLyricsPayload{
					Content: stringValue(lyricsMap["content"]), Translation: stringValue(lyricsMap["translation"]),
					Format: stringValue(lyricsMap["format"]), Language: stringValue(lyricsMap["language"]),
					EditSummary: stringValue(lyricsMap["edit_summary"]),
				}
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

func albumImportSessionAlbumTitle(session model.AlbumImportSession, payload map[string]any) string {
	if session.TargetAlbum != nil && strings.TrimSpace(session.TargetAlbum.Title) != "" {
		return strings.TrimSpace(session.TargetAlbum.Title)
	}
	if request, ok := payload["commit_request"].(map[string]any); ok {
		if album, ok := request["album"].(map[string]any); ok {
			if title := strings.TrimSpace(stringValue(album["title"])); title != "" {
				return title
			}
		}
	}
	if title := strings.TrimSpace(stringValue(payload["derived_album_title"])); title != "" {
		return title
	}
	for _, file := range session.Files {
		if file.Role == AlbumImportFileRoleArchive && strings.TrimSpace(file.FileName) != "" {
			return strings.TrimSpace(strings.TrimSuffix(file.FileName, filepath.Ext(file.FileName)))
		}
	}
	return ""
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

	inputMode := normalizeAlbumImportInputMode(input.InputMode)
	if !isAlbumImportInputModeAllowed(inputMode) {
		return model.AlbumImportSession{}, apperr.BadRequest("validation.invalid_request", "invalid import input mode")
	}

	session := model.AlbumImportSession{
		UserID:      &user.ID,
		InputMode:   inputMode,
		PayloadJSON: string(payloadJSON),
	}
	payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	if artistID := strings.TrimSpace(input.ArtistID); artistID != "" {
		payload["artist_id"] = artistID
	}
	if artistName := strings.TrimSpace(input.ArtistName); artistName != "" {
		payload["artist_name"] = artistName
	}
	applyAlbumImportSessionState(&session, status, payload)
	payloadJSON, err = json.Marshal(payload)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	session.PayloadJSON = string(payloadJSON)
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	session.ExpiresAt = &expiresAt
	if err := s.db.Create(&session).Error; err != nil {
		return model.AlbumImportSession{}, err
	}
	s.updateAlbumImportNotification(session)
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

func (s *Service) ListAlbumImportSessionsForUser(user authctx.CurrentUser) ([]model.AlbumImportSession, error) {
	sessions, _, err := s.ListAlbumImportSessionsPageForUser(user, 1, 100)
	return sessions, err
}

func (s *Service) ListAlbumImportSessionsPageForUser(user authctx.CurrentUser, page, pageSize int) ([]model.AlbumImportSession, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	if err := s.db.Model(&model.AlbumImportSession{}).
		Where("user_id = ? AND status IN ? AND updated_at < ?", user.ID,
			[]string{AlbumImportStatusPendingUpload, AlbumImportStatusUploading}, time.Now().UTC().Add(-30*time.Minute)).
		Updates(map[string]any{
			"status":        AlbumImportStatusNeedsAttention,
			"stage":         AlbumImportStageReady,
			"error_message": "上传已暂停，请重新选择源文件恢复",
		}).Error; err != nil {
		return nil, 0, err
	}
	base := s.db.Model(&model.AlbumImportSession{}).Where("user_id = ?", user.ID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var sessions []model.AlbumImportSession
	if err := s.db.Preload("Files").Preload("Job").Preload("TargetAlbum").
		Where("user_id = ?", user.ID).
		Order("created_at DESC").
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Find(&sessions).Error; err != nil {
		return nil, 0, err
	}
	for _, session := range sessions {
		s.updateAlbumImportNotification(session)
	}
	return sessions, total, nil
}

func (s *Service) DeleteAlbumImportRecord(user authctx.CurrentUser, id uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var session model.AlbumImportSession
		if err := tx.Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.import_not_found", "Import session not found")
			}
			return err
		}
		if session.Status != AlbumImportStatusCommitted && session.Status != AlbumImportStatusCanceled {
			return apperr.Unprocessable("music.import_invalid_status", "Cancel the import before deleting its record")
		}
		if err := tx.Where("import_id = ?", id).Delete(&model.AlbumImportJob{}).Error; err != nil {
			return err
		}
		if err := tx.Where("import_id = ?", id).Delete(&model.AlbumImportFile{}).Error; err != nil {
			return err
		}
		if err := tx.Where("recipient_id = ? AND source_type = ? AND source_id = ?", user.ID, "music_album_import", id).Delete(&model.Notification{}).Error; err != nil {
			return err
		}
		return tx.Delete(&session).Error
	})
}

func loadAlbumImportSession(db *gorm.DB, id uuid.UUID, userID *uuid.UUID) (model.AlbumImportSession, error) {
	var session model.AlbumImportSession
	query := db.Preload("Files").Preload("Job").Preload("TargetAlbum")
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

func normalizeAlbumImportInputMode(inputMode string) string {
	if strings.TrimSpace(inputMode) == "" {
		return AlbumImportInputModeAuto
	}
	return strings.TrimSpace(strings.ToLower(inputMode))
}

func isAlbumImportInputModeAllowed(inputMode string) bool {
	switch inputMode {
	case AlbumImportInputModeAuto, AlbumImportInputModeArchive, AlbumImportInputModeFiles, AlbumImportInputModeFolder:
		return true
	default:
		return false
	}
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
