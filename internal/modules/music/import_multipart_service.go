package music

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) StartAlbumImportMultipart(user authctx.CurrentUser, id uuid.UUID, input StartAlbumImportMultipartInput) (AlbumImportMultipartDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportMultipartDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return AlbumImportMultipartDTO{}, err
	}

	fileName := strings.TrimSpace(input.FileName)
	role, format, formatErr := detectAlbumImportFileRole(fileName)
	if fileName == "" || formatErr != nil || role != AlbumImportFileRoleArchive {
		return AlbumImportMultipartDTO{}, apperr.BadRequest("validation.invalid_request", "archive format is not supported")
	}
	limits := albumImportUploadLimitsFromEnv()
	if input.FileSize <= 0 || input.FileSize > limits.MaxFileBytes || input.FileSize > limits.MaxTotalBytes {
		return AlbumImportMultipartDTO{}, apperr.BadRequest("validation.invalid_request", "archive file size is invalid")
	}

	var out AlbumImportMultipartDTO
	var createdUpload *model.AlbumImportFile
	var overwrittenUpload *model.AlbumImportFile
	var preparedArchiveID uuid.UUID
	var preparedObjectKey, preparedUploadID, preparedContentType string
	preflight, err := s.GetAlbumImportSessionForUser(user, id)
	if err != nil {
		return AlbumImportMultipartDTO{}, err
	}
	if !isAlbumImportMultipartStartStatus(preflight.Status) {
		return AlbumImportMultipartDTO{}, apperr.Unprocessable("music.import_invalid_status", "Import session cannot start upload")
	}
	preflightPayload, err := readAlbumImportPayloadMap(preflight.PayloadJSON)
	if err != nil {
		return AlbumImportMultipartDTO{}, err
	}
	preflightState := albumImportMultipartStateFromPayload(preflightPayload)
	var preflightArchive *model.AlbumImportFile
	for index := range preflight.Files {
		if preflight.Files[index].Role == AlbumImportFileRoleArchive {
			preflightArchive = &preflight.Files[index]
			break
		}
	}
	if preflightArchive != nil && preflightArchive.UploadStatus == AlbumImportFileUploadStatusCompleting {
		return AlbumImportMultipartDTO{}, apperr.Unprocessable("music.import_invalid_status", "Album import file is completing upload")
	}
	if !(preflightState.FileName == fileName && preflightState.FileSize == input.FileSize && preflightState.UploadID != "" && preflightState.ObjectKey != "") {
		preparedArchiveID = uuid.New()
		if preflightArchive != nil {
			preparedArchiveID = preflightArchive.ID
		}
		preparedObjectKey = albumImportSourceKey(user.ID, id, preparedArchiveID, format)
		preparedContentType = strings.TrimSpace(input.ContentType)
		if preparedContentType == "" {
			preparedContentType = archiveContentType(format)
		}
		preparedUploadID, err = s.albumImportMultipart.CreateMultipartUpload(preparedObjectKey, preparedContentType)
		if err != nil {
			return AlbumImportMultipartDTO{}, err
		}
		createdUpload = &model.AlbumImportFile{SourceKey: preparedObjectKey, UploadID: preparedUploadID}
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, id, user.ID)
		if err != nil {
			return err
		}
		if !isAlbumImportMultipartStartStatus(session.Status) {
			return apperr.Unprocessable("music.import_invalid_status", "Import session cannot start upload")
		}

		payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
		if err != nil {
			return err
		}
		var archiveFile model.AlbumImportFile
		fileErr := tx.First(&archiveFile, "import_id = ? AND role = ?", session.ID, AlbumImportFileRoleArchive).Error
		if fileErr != nil && fileErr != gorm.ErrRecordNotFound {
			return fileErr
		}
		if fileErr == gorm.ErrRecordNotFound {
			archiveFile.ID = preparedArchiveID
		}
		if fileErr == nil && archiveFile.UploadStatus == AlbumImportFileUploadStatusCompleting {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file is completing upload")
		}
		state := albumImportMultipartStateFromPayload(payload)
		if state.FileName == fileName && state.FileSize == input.FileSize && state.UploadID != "" && state.ObjectKey != "" {
			if err := saveLegacyAlbumImportFile(tx, &archiveFile, session.ID, state, input.ContentType); err != nil {
				return err
			}
			if session.Status == AlbumImportStatusFailed {
				applyAlbumImportSessionState(&session, AlbumImportStatusUploading, payload)
				payloadJSON, err := json.Marshal(payload)
				if err != nil {
					return err
				}
				session.PayloadJSON = string(payloadJSON)
				if err := tx.Save(&session).Error; err != nil {
					return err
				}
			}
			out = buildAlbumImportMultipartDTO(session.ID, state)
			return nil
		}
		if fileErr == nil && archiveFile.SourceKey != "" && archiveFile.UploadID != "" {
			previous := archiveFile
			overwrittenUpload = &previous
		}

		state = albumImportMultipartState{
			FileName:       fileName,
			FileSize:       input.FileSize,
			ObjectKey:      preparedObjectKey,
			UploadID:       preparedUploadID,
			Format:         format,
			PartSize:       albumImportMultipartPartSize,
			CompletedParts: []AlbumImportMultipartPartDTO{},
		}
		writeAlbumImportMultipartState(payload, state)
		applyAlbumImportSessionState(&session, AlbumImportStatusUploading, payload)
		session.InputMode = AlbumImportInputModeArchive
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		session.PayloadJSON = string(payloadJSON)
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		if err := saveLegacyAlbumImportFile(tx, &archiveFile, session.ID, state, preparedContentType); err != nil {
			return err
		}
		out = buildAlbumImportMultipartDTO(session.ID, state)
		return nil
	})
	if err != nil {
		if createdUpload != nil {
			s.abortAlbumImportMultipartOrRecord(id, *createdUpload)
		}
		return AlbumImportMultipartDTO{}, err
	}
	if overwrittenUpload != nil {
		s.cleanupAlbumImportObject(*overwrittenUpload)
	}
	return out, nil
}

func (s *Service) CreateAlbumImportMultipartPartUpload(user authctx.CurrentUser, id uuid.UUID, partNumber int, input CreateAlbumImportMultipartPartInput) (AlbumImportMultipartPartUploadDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportMultipartPartUploadDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
	}
	if partNumber <= 0 {
		return AlbumImportMultipartPartUploadDTO{}, apperr.BadRequest("validation.invalid_request", "part number is invalid")
	}

	session, err := s.GetAlbumImportSessionForUser(user, id)
	if err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
	}
	if !isAlbumImportMultipartPartUploadStatus(session.Status) {
		return AlbumImportMultipartPartUploadDTO{}, apperr.Unprocessable("music.import_invalid_status", "Import session is not uploading")
	}
	payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
	}
	state := albumImportMultipartStateFromPayload(payload)
	if state.ObjectKey == "" || state.UploadID == "" {
		return AlbumImportMultipartPartUploadDTO{}, apperr.BadRequest("validation.invalid_request", "multipart upload has not started")
	}
	_ = input
	signedURL, err := s.albumImportMultipart.PresignUploadPart(state.ObjectKey, state.UploadID, partNumber, 15*time.Minute)
	if err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
	}
	return AlbumImportMultipartPartUploadDTO{PartNumber: partNumber, UploadURL: signedURL}, nil
}

func (s *Service) CompleteAlbumImportMultipartPart(user authctx.CurrentUser, id uuid.UUID, partNumber int, input CompleteAlbumImportMultipartPartInput) (AlbumImportMultipartDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportMultipartDTO{}, apperr.Unauthorized("Login required")
	}
	if partNumber <= 0 {
		return AlbumImportMultipartDTO{}, apperr.BadRequest("validation.invalid_request", "part number is invalid")
	}
	etag := strings.TrimSpace(input.ETag)
	if etag == "" || input.Size <= 0 {
		return AlbumImportMultipartDTO{}, apperr.BadRequest("validation.invalid_request", "completed part is invalid")
	}

	var out AlbumImportMultipartDTO
	err := s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, id, user.ID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusUploading {
			return apperr.Unprocessable("music.import_invalid_status", "Import session is not uploading")
		}
		payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
		if err != nil {
			return err
		}
		state := albumImportMultipartStateFromPayload(payload)
		if state.ObjectKey == "" || state.UploadID == "" {
			return apperr.BadRequest("validation.invalid_request", "multipart upload has not started")
		}
		replaced := false
		for i := range state.CompletedParts {
			if state.CompletedParts[i].PartNumber == partNumber {
				state.CompletedParts[i] = AlbumImportMultipartPartDTO{PartNumber: partNumber, ETag: etag, Size: input.Size}
				replaced = true
				break
			}
		}
		if !replaced {
			state.CompletedParts = append(state.CompletedParts, AlbumImportMultipartPartDTO{PartNumber: partNumber, ETag: etag, Size: input.Size})
		}
		sort.Slice(state.CompletedParts, func(i, j int) bool {
			return state.CompletedParts[i].PartNumber < state.CompletedParts[j].PartNumber
		})
		writeAlbumImportMultipartState(payload, state)
		applyAlbumImportSessionState(&session, AlbumImportStatusUploading, payload)
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		session.PayloadJSON = string(payloadJSON)
		var archiveFile model.AlbumImportFile
		if err := tx.First(&archiveFile, "import_id = ? AND role = ?", session.ID, AlbumImportFileRoleArchive).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.import_file_not_found", "Album import file not found")
			}
			return err
		}
		if err := saveLegacyAlbumImportFile(tx, &archiveFile, session.ID, state, archiveFile.ContentType); err != nil {
			return err
		}
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = buildAlbumImportMultipartDTO(session.ID, state)
		return nil
	})
	if err != nil {
		return AlbumImportMultipartDTO{}, err
	}
	return out, nil
}

func (s *Service) CompleteAlbumImportMultipart(user authctx.CurrentUser, id uuid.UUID) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return model.AlbumImportSession{}, err
	}

	current, err := s.GetAlbumImportSessionForUser(user, id)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	if current.Status == AlbumImportStatusQueued {
		return current, nil
	}
	if current.Status == AlbumImportStatusCanceled || current.Status == AlbumImportStatusCommitted {
		return model.AlbumImportSession{}, apperr.Unprocessable("music.import_invalid_status", "Import session cannot be queued")
	}

	_, payload, state, _, err := s.loadCompletableAlbumImportMultipart(user, id)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	parts := append([]AlbumImportMultipartPartDTO(nil), state.CompletedParts...)
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})

	var upload model.AlbumImportFile
	err = s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, id, user.ID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusUploading {
			return apperr.Unprocessable("music.import_invalid_status", "Import session is not uploading")
		}
		if err := tx.First(&upload, "import_id = ? AND role = ?", session.ID, AlbumImportFileRoleArchive).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.import_file_not_found", "Album import file not found")
			}
			return err
		}
		if upload.UploadStatus != AlbumImportFileUploadStatusUploading && upload.UploadStatus != AlbumImportFileUploadStatusCompleting {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file is not uploading")
		}
		if upload.UploadStatus == AlbumImportFileUploadStatusUploading {
			upload.UploadStatus = AlbumImportFileUploadStatusCompleting
			return tx.Save(&upload).Error
		}
		return nil
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	if err := s.reconcileAlbumImportObject(upload, parts); err != nil {
		if errors.Is(err, errAlbumImportObjectSizeMismatch) {
			if err := s.failLegacyAlbumImportUpload(user.ID, id, upload.ID, payload, "uploaded file size does not match"); err != nil {
				return model.AlbumImportSession{}, err
			}
			s.deleteAlbumImportObjectOrRecord(upload)
			return model.AlbumImportSession{}, apperr.Unprocessable("music.import_file_size_mismatch", "Uploaded file size does not match")
		}
		return model.AlbumImportSession{}, err
	}
	delete(payload, "error_message")
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, id, user.ID)
		if err != nil {
			return err
		}
		if session.Status == AlbumImportStatusQueued {
			return nil
		}
		if session.Status != AlbumImportStatusUploading {
			return apperr.Unprocessable("music.import_invalid_status", "Import session is not uploading")
		}
		var archiveFile model.AlbumImportFile
		if err := tx.First(&archiveFile, "import_id = ? AND role = ?", session.ID, AlbumImportFileRoleArchive).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.import_file_not_found", "Album import file not found")
			}
			return err
		}
		if archiveFile.UploadStatus != AlbumImportFileUploadStatusCompleting {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file cannot finish upload")
		}
		archiveFile.UploadStatus = AlbumImportFileUploadStatusUploaded
		if err := tx.Save(&archiveFile).Error; err != nil {
			return err
		}
		session.PayloadJSON = string(payloadJSON)
		return queueAlbumImportSession(tx, &session, true)
	})
	if err != nil {
		_ = s.markAlbumImportFailed(id, "upload failed")
		return model.AlbumImportSession{}, err
	}
	return s.GetAlbumImportSessionForUser(user, id)
}

func (s *Service) failLegacyAlbumImportUpload(userID, sessionID, fileID uuid.UUID, payload map[string]any, message string) error {
	delete(payload, "multipart_upload_id")
	delete(payload, "multipart_object_key")
	payload["multipart_completed_parts"] = []AlbumImportMultipartPartDTO{}
	payload["error_message"] = message
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.AlbumImportFile{}).Where("id = ? AND import_id = ?", fileID, sessionID).Updates(map[string]any{
			"upload_status": AlbumImportFileUploadStatusFailed, "upload_id": "", "error_message": message,
		})
		if result.Error != nil {
			return result.Error
		}
		return tx.Model(&model.AlbumImportSession{}).Where("id = ? AND user_id = ?", sessionID, userID).Updates(map[string]any{
			"status": AlbumImportStatusFailed, "stage": AlbumImportStageFailed, "error_message": message, "payload_json": string(payloadJSON), "expires_at": albumImportFailureExpiresAt(),
		}).Error
	})
}

func (s *Service) loadCompletableAlbumImportMultipart(user authctx.CurrentUser, id uuid.UUID) (model.AlbumImportSession, map[string]any, albumImportMultipartState, string, error) {
	session, err := s.GetAlbumImportSessionForUser(user, id)
	if err != nil {
		return model.AlbumImportSession{}, nil, albumImportMultipartState{}, "", err
	}
	if session.Status != AlbumImportStatusUploading {
		return model.AlbumImportSession{}, nil, albumImportMultipartState{}, "", apperr.Unprocessable("music.import_invalid_status", "Import session is not uploading")
	}
	payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		return model.AlbumImportSession{}, nil, albumImportMultipartState{}, "", err
	}
	archiveName := strings.TrimSpace(stringValue(payload["archive_name"]))
	if archiveName == "" {
		return model.AlbumImportSession{}, nil, albumImportMultipartState{}, "", apperr.Unprocessable("music.import_invalid_status", "Archive name is missing")
	}
	state := albumImportMultipartStateFromPayload(payload)
	if state.ObjectKey == "" || state.UploadID == "" || len(state.CompletedParts) == 0 {
		return model.AlbumImportSession{}, nil, albumImportMultipartState{}, "", apperr.Unprocessable("music.import_invalid_status", "Multipart upload is incomplete")
	}
	return session, payload, state, archiveName, nil
}

func albumImportCompletedPartBytes(value any) int64 {
	var total int64
	switch parts := value.(type) {
	case []AlbumImportMultipartPartDTO:
		for _, part := range parts {
			total += part.Size
		}
	case []map[string]any:
		for _, part := range parts {
			total += int64Value(part["size"])
		}
	case []any:
		for _, rawPart := range parts {
			if part, ok := rawPart.(map[string]any); ok {
				total += int64Value(part["size"])
			}
		}
	}
	return total
}

func albumImportDerivedTrackCount(value any) int64 {
	switch tracks := value.(type) {
	case []map[string]any:
		return int64(len(tracks))
	case []any:
		return int64(len(tracks))
	case []AlbumImportDTOTrack:
		return int64(len(tracks))
	default:
		return 0
	}
}

type albumImportMultipartState struct {
	FileName       string
	FileSize       int64
	Format         string
	ObjectKey      string
	UploadID       string
	PartSize       int64
	CompletedParts []AlbumImportMultipartPartDTO
}

func albumImportMultipartStateFromPayload(payload map[string]any) albumImportMultipartState {
	state := albumImportMultipartState{
		FileName:       stringValue(payload["multipart_file_name"]),
		FileSize:       int64Value(payload["multipart_file_size"]),
		Format:         stringValue(payload["multipart_format"]),
		ObjectKey:      stringValue(payload["multipart_object_key"]),
		UploadID:       stringValue(payload["multipart_upload_id"]),
		PartSize:       int64Value(payload["multipart_part_size"]),
		CompletedParts: []AlbumImportMultipartPartDTO{},
	}
	if state.PartSize <= 0 {
		state.PartSize = albumImportMultipartPartSize
	}
	rawParts, ok := payload["multipart_completed_parts"].([]any)
	if !ok {
		return state
	}
	for _, rawPart := range rawParts {
		partMap, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		part := AlbumImportMultipartPartDTO{
			PartNumber: int(int64Value(partMap["partNumber"])),
			ETag:       stringValue(partMap["etag"]),
			Size:       int64Value(partMap["size"]),
		}
		if part.PartNumber > 0 && part.ETag != "" && part.Size > 0 {
			state.CompletedParts = append(state.CompletedParts, part)
		}
	}
	sort.Slice(state.CompletedParts, func(i, j int) bool {
		return state.CompletedParts[i].PartNumber < state.CompletedParts[j].PartNumber
	})
	return state
}

func writeAlbumImportMultipartState(payload map[string]any, state albumImportMultipartState) {
	payload["archive_name"] = state.FileName
	payload["multipart_file_name"] = state.FileName
	payload["multipart_file_size"] = state.FileSize
	payload["multipart_format"] = state.Format
	payload["multipart_object_key"] = state.ObjectKey
	payload["multipart_upload_id"] = state.UploadID
	payload["multipart_part_size"] = state.PartSize
	payload["multipart_completed_parts"] = state.CompletedParts
}

func buildAlbumImportMultipartDTO(importID uuid.UUID, state albumImportMultipartState) AlbumImportMultipartDTO {
	parts := append([]AlbumImportMultipartPartDTO{}, state.CompletedParts...)
	return AlbumImportMultipartDTO{
		ImportID:       importID.String(),
		FileName:       state.FileName,
		FileSize:       state.FileSize,
		ObjectKey:      state.ObjectKey,
		PartSize:       state.PartSize,
		CompletedParts: parts,
	}
}
