package music

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/storage"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var albumImportCreateAlbumHook func(*gorm.DB, *model.Album) error

const (
	maxAlbumImportArchiveSize    int64 = 2 * 1024 * 1024 * 1024
	albumImportMultipartPartSize int64 = 16 * 1024 * 1024
)

func buildAlbumImportDTO(session model.AlbumImportSession) AlbumImportDTO {
	payload := map[string]any{}
	if strings.TrimSpace(session.PayloadJSON) != "" {
		_ = json.Unmarshal([]byte(session.PayloadJSON), &payload)
	}

	dto := AlbumImportDTO{
		ImportID:          session.ID.String(),
		Status:            session.Status,
		ArchiveName:       stringValue(payload["archive_name"]),
		UploadProgress:    floatValue(payload["upload_progress"]),
		UploadSpeed:       floatValue(payload["upload_speed"]),
		CoverURL:          stringValue(payload["cover_url"]),
		CoverKey:          stringValue(payload["cover_key"]),
		DerivedAlbumTitle: stringValue(payload["derived_album_title"]),
		DerivedCover:      stringValue(payload["derived_cover"]),
		LastSyncedAt:      session.UpdatedAt.Format(time.RFC3339),
		ErrorMessage:      stringValue(payload["error_message"]),
		DerivedTracks:     []AlbumImportDTOTrack{},
	}

	if rawTracks, ok := payload["derived_tracks"].([]any); ok {
		for _, rawTrack := range rawTracks {
			trackMap, ok := rawTrack.(map[string]any)
			if !ok {
				continue
			}
			dto.DerivedTracks = append(dto.DerivedTracks, AlbumImportDTOTrack{
				Title:    stringValue(trackMap["title"]),
				AudioKey: stringValue(trackMap["audio_key"]),
				AudioURL: stringValue(trackMap["audio_url"]),
				Origin:   stringValue(trackMap["origin"]),
			})
		}
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
		Status:      status,
		PayloadJSON: string(payloadJSON),
	}
	if err := s.db.Create(&session).Error; err != nil {
		return model.AlbumImportSession{}, err
	}
	return session, nil
}

func (s *Service) UploadAlbumImportArchive(user authctx.CurrentUser, id uuid.UUID, archiveName string, reader io.Reader) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	if strings.TrimSpace(archiveName) == "" {
		return model.AlbumImportSession{}, apperr.BadRequest("validation.invalid_request", "archive file name is required")
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	payloadPatch, err := s.deriveAlbumImportPayload(user, strings.TrimSpace(archiveName), body)
	if err != nil {
		return model.AlbumImportSession{}, err
	}

	var out model.AlbumImportSession
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var session model.AlbumImportSession
		if err := tx.First(&session, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.import_not_found", "Import session not found")
			}
			return err
		}
		if session.Status != AlbumImportStatusPendingUpload {
			return apperr.Unprocessable("music.import_invalid_status", "Import session is not pending upload")
		}

		payload := map[string]any{}
		if strings.TrimSpace(session.PayloadJSON) != "" {
			if err := json.Unmarshal([]byte(session.PayloadJSON), &payload); err != nil {
				return apperr.BadRequest("validation.invalid_request", "payload is not valid JSON")
			}
		}
		for key, value := range payloadPatch {
			payload[key] = value
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}

		session.Status = AlbumImportStatusReady
		session.PayloadJSON = string(payloadJSON)
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = session
		return nil
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	return out, nil
}

func (s *Service) StartAlbumImportMultipart(user authctx.CurrentUser, id uuid.UUID, input StartAlbumImportMultipartInput) (AlbumImportMultipartDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportMultipartDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return AlbumImportMultipartDTO{}, err
	}

	fileName := strings.TrimSpace(input.FileName)
	if fileName == "" || strings.ToLower(filepath.Ext(fileName)) != ".zip" {
		return AlbumImportMultipartDTO{}, apperr.BadRequest("validation.invalid_request", "archive must be a zip file")
	}
	if input.FileSize <= 0 || input.FileSize > maxAlbumImportArchiveSize {
		return AlbumImportMultipartDTO{}, apperr.BadRequest("validation.invalid_request", "archive file size is invalid")
	}

	var out AlbumImportMultipartDTO
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var session model.AlbumImportSession
		if err := tx.First(&session, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.import_not_found", "Import session not found")
			}
			return err
		}
		if !isAlbumImportMultipartStartStatus(session.Status) {
			return apperr.Unprocessable("music.import_invalid_status", "Import session cannot start upload")
		}

		payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
		if err != nil {
			return err
		}
		state := albumImportMultipartStateFromPayload(payload)
		if state.FileName == fileName && state.FileSize == input.FileSize && state.UploadID != "" && state.ObjectKey != "" {
			out = buildAlbumImportMultipartDTO(session.ID, state)
			return nil
		}

		objectKey := storage.BuildMusicUploadKey("album-imports", user.ID.String(), uuid.NewString()+".zip", time.Now())
		contentType := strings.TrimSpace(input.ContentType)
		if contentType == "" {
			contentType = "application/zip"
		}
		uploadID, err := s.albumImportMultipart.CreateMultipartUpload(objectKey, contentType)
		if err != nil {
			return err
		}
		state = albumImportMultipartState{
			FileName:       fileName,
			FileSize:       input.FileSize,
			ObjectKey:      objectKey,
			UploadID:       uploadID,
			PartSize:       albumImportMultipartPartSize,
			CompletedParts: []AlbumImportMultipartPartDTO{},
		}
		writeAlbumImportMultipartState(payload, state)
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		session.Status = AlbumImportStatusUploading
		session.PayloadJSON = string(payloadJSON)
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

	session, err := s.GetAlbumImportSession(id)
	if err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
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
		var session model.AlbumImportSession
		if err := tx.First(&session, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.import_not_found", "Import session not found")
			}
			return err
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
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		session.Status = AlbumImportStatusUploading
		session.PayloadJSON = string(payloadJSON)
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

func (s *Service) GetAlbumImportSession(id uuid.UUID) (model.AlbumImportSession, error) {
	var session model.AlbumImportSession
	if err := s.db.First(&session, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.AlbumImportSession{}, apperr.NotFound("music.import_not_found", "Import session not found")
		}
		return model.AlbumImportSession{}, err
	}
	return session, nil
}

func (s *Service) CommitAlbumImportSession(user authctx.CurrentUser, id uuid.UUID, input CommitAlbumImportSessionInput) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}

	var out model.AlbumImportSession
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var session model.AlbumImportSession
		if err := tx.First(&session, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.import_not_found", "Import session not found")
			}
			return err
		}
		if session.Status != AlbumImportStatusReady {
			return apperr.Unprocessable("music.import_invalid_status", "Import session is not ready")
		}

		payload := AlbumImportPayload{
			Artist: input.Artist,
			Album:  input.Album,
		}
		if strings.TrimSpace(payload.Album.Title) == "" {
			return apperr.BadRequest("validation.invalid_request", "album title is required")
		}
		var artist model.Artist
		artistID := strings.TrimSpace(input.ArtistID)
		if artistID != "" {
			parsedArtistID, err := uuid.Parse(artistID)
			if err != nil {
				return apperr.BadRequest("validation.invalid_request", "artist_id must be a valid UUID")
			}
			if err := tx.First(&artist, "id = ?", parsedArtistID).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return apperr.NotFound("music.artist_not_found", "Artist not found")
				}
				return err
			}
		} else {
			if strings.TrimSpace(payload.Artist.Name) == "" {
				return apperr.BadRequest("validation.invalid_request", "artist name is required")
			}
			artist = model.Artist{
				Name:           strings.TrimSpace(payload.Artist.Name),
				LegalName:      strings.TrimSpace(payload.Artist.LegalName),
				StageNamesJSON: mustMarshalStageNames(payload.Artist.StageNames),
				BirthPlace:     strings.TrimSpace(payload.Artist.BirthPlace),
				EntryStatus:    "open",
			}
			if err := createAlbumImportArtist(tx, &artist); err != nil {
				return err
			}
		}

		var sessionPayload map[string]any
		if strings.TrimSpace(session.PayloadJSON) != "" {
			_ = json.Unmarshal([]byte(session.PayloadJSON), &sessionPayload)
		}

		coverURL := ""
		if sessionPayload != nil {
			if url, ok := sessionPayload["cover_url"].(string); ok && url != "" {
				coverURL = url
			} else if url, ok := sessionPayload["derived_cover"].(string); ok && url != "" {
				coverURL = url
			}
		}

		album := model.Album{
			Title:       strings.TrimSpace(payload.Album.Title),
			ReleaseYear: payload.Album.ReleaseYear,
			Year:        payload.Album.ReleaseYear,
			CoverURL:    coverURL,
			Status:      "open",
			EntryStatus: "open",
			AlbumType:   "album",
			UploadedBy:  &user.ID,
		}
		if err := createAlbumImportAlbum(tx, &album); err != nil {
			return err
		}
		if err := tx.Model(&album).Association("Artists").Append(&artist); err != nil {
			return err
		}

		for _, track := range payload.Album.Tracks {
			audioURL := ""
			if sessionPayload != nil {
				if rawTracks, ok := sessionPayload["derived_tracks"].([]any); ok {
					for _, rawTrack := range rawTracks {
						trackMap, ok := rawTrack.(map[string]any)
						if !ok {
							continue
						}
						if strings.TrimSpace(stringValue(trackMap["title"])) == strings.TrimSpace(track.Title) {
							audioURL = stringValue(trackMap["audio_url"])
							break
						}
					}
				}
			}

			song := model.Song{
				Title:       strings.TrimSpace(track.Title),
				TrackNumber: track.TrackNumber,
				AlbumID:     &album.ID,
				Status:      "open",
				AudioURL:    audioURL,
				AudioSource: "local",
				UploadedBy:  &user.ID,
			}
			if err := tx.Create(&song).Error; err != nil {
				return err
			}
			if err := tx.Model(&song).Association("Artists").Append(&artist); err != nil {
				return err
			}
		}

		now := time.Now()
		session.Status = AlbumImportStatusCommitted
		session.CommittedAt = &now
		session.CommittedBy = &user.ID
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = session
		return nil
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	return out, nil
}

func createAlbumImportArtist(tx *gorm.DB, artist *model.Artist) error {
	return tx.Create(artist).Error
}

func createAlbumImportAlbum(tx *gorm.DB, album *model.Album) error {
	if albumImportCreateAlbumHook != nil {
		if err := albumImportCreateAlbumHook(tx, album); err != nil {
			return err
		}
	}
	return tx.Create(album).Error
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

func isAlbumImportMultipartStartStatus(status string) bool {
	switch status {
	case AlbumImportStatusPendingUpload, AlbumImportStatusUploading, AlbumImportStatusFailed:
		return true
	default:
		return false
	}
}

type albumImportMultipartState struct {
	FileName       string
	FileSize       int64
	ObjectKey      string
	UploadID       string
	PartSize       int64
	CompletedParts []AlbumImportMultipartPartDTO
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

func albumImportMultipartStateFromPayload(payload map[string]any) albumImportMultipartState {
	state := albumImportMultipartState{
		FileName:       stringValue(payload["multipart_file_name"]),
		FileSize:       int64Value(payload["multipart_file_size"]),
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
	payload["multipart_object_key"] = state.ObjectKey
	payload["multipart_upload_id"] = state.UploadID
	payload["multipart_part_size"] = state.PartSize
	payload["multipart_completed_parts"] = state.CompletedParts
}

func buildAlbumImportMultipartDTO(importID uuid.UUID, state albumImportMultipartState) AlbumImportMultipartDTO {
	parts := append([]AlbumImportMultipartPartDTO(nil), state.CompletedParts...)
	return AlbumImportMultipartDTO{
		ImportID:       importID.String(),
		FileName:       state.FileName,
		FileSize:       state.FileSize,
		ObjectKey:      state.ObjectKey,
		PartSize:       state.PartSize,
		CompletedParts: parts,
	}
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

func (s *Service) deriveAlbumImportPayload(user authctx.CurrentUser, archiveName string, body []byte) (map[string]any, error) {
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", "archive must be a valid zip file")
	}

	derivedTracks := make([]map[string]any, 0)
	var coverURL string

	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		// Filter out __MACOSX and hidden files/directories starting with .
		segments := strings.Split(filepath.ToSlash(file.Name), "/")
		ignored := false
		for _, segment := range segments {
			if (strings.HasPrefix(segment, ".") && segment != "." && segment != "..") || segment == "__MACOSX" {
				ignored = true
				break
			}
		}
		if ignored {
			continue
		}

		base := filepath.Base(file.Name)
		ext := strings.ToLower(filepath.Ext(base))

		// Check if it's an audio track
		if trackTitle, trackNumber, ok := deriveTrackFromArchiveEntry(file.Name); ok {
			audioKey := ""
			audioURL := ""
			if rc, err := file.Open(); err == nil {
				audioBytes, readErr := io.ReadAll(rc)
				rc.Close()
				if readErr == nil {
					filename := uuid.NewString() + ext
					uploadedURL, uploadedKey, uploadErr := s.storeImportedAudio(user, filename, ext, audioBytes)
					if uploadErr == nil {
						audioURL = uploadedURL
						audioKey = uploadedKey
					}
				}
			}
			derivedTracks = append(derivedTracks, map[string]any{
				"title":        trackTitle,
				"track_number": trackNumber,
				"audio_key":    audioKey,
				"audio_url":    audioURL,
				"origin":       file.Name,
			})
			continue
		}

		// Check if it's a cover image
		if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" {
			lowerBase := strings.ToLower(base)
			// Prefer files with cover, folder, front in name, or fallback to any first image
			if strings.Contains(lowerBase, "cover") || strings.Contains(lowerBase, "folder") || strings.Contains(lowerBase, "front") || coverURL == "" {
				rc, err := file.Open()
				if err != nil {
					continue
				}
				imgBytes, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					continue
				}

				uploadedURL, err := s.storeImportedCover(user, uuid.NewString()+ext, ext, imgBytes)
				if err != nil {
					continue
				}
				coverURL = uploadedURL
			}
		}
	}

	return map[string]any{
		"archive_name":        archiveName,
		"derived_album_title": strings.TrimSpace(strings.TrimSuffix(archiveName, filepath.Ext(archiveName))),
		"derived_tracks":      derivedTracks,
		"derived_cover":       coverURL,
		"upload_progress":     100,
		"upload_speed":        0,
	}, nil
}

func (s *Service) storeImportedAudio(user authctx.CurrentUser, filename string, ext string, content []byte) (string, string, error) {
	if s.s3 != nil && strings.EqualFold(strings.TrimSpace(os.Getenv("STORAGE_TYPE")), "s3") {
		bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
		urlPrefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
		if bucket != "" && urlPrefix != "" {
			key := storage.BuildMusicUploadKey("audio", user.ID.String(), filename, time.Now())
			contentType := "audio/mpeg"
			switch strings.ToLower(ext) {
			case ".flac":
				contentType = "audio/flac"
			case ".wav":
				contentType = "audio/wav"
			case ".m4a":
				contentType = "audio/x-m4a"
			case ".aac":
				contentType = "audio/aac"
			case ".ogg":
				contentType = "audio/ogg"
			}
			if _, err := s.s3.PutObject(&s3.PutObjectInput{
				Bucket:      aws.String(bucket),
				Key:         aws.String(key),
				Body:        bytes.NewReader(content),
				ContentType: aws.String(contentType),
				ACL:         aws.String("public-read"),
			}); err == nil {
				return urlPrefix + "/" + key, key, nil
			}
		}
	}
	return "", "", nil
}

func (s *Service) storeImportedCover(user authctx.CurrentUser, filename string, ext string, content []byte) (string, error) {
	if s.s3 != nil && strings.EqualFold(strings.TrimSpace(os.Getenv("STORAGE_TYPE")), "s3") {
		bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
		urlPrefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
		if bucket != "" && urlPrefix != "" {
			key := storage.BuildMusicUploadKey("covers", user.ID.String(), filename, time.Now())
			contentType := "image/jpeg"
			switch strings.ToLower(ext) {
			case ".png":
				contentType = "image/png"
			case ".webp":
				contentType = "image/webp"
			}
			if _, err := s.s3.PutObject(&s3.PutObjectInput{
				Bucket:      aws.String(bucket),
				Key:         aws.String(key),
				Body:        bytes.NewReader(content),
				ContentType: aws.String(contentType),
				ACL:         aws.String("public-read"),
			}); err == nil {
				return urlPrefix + "/" + key, nil
			}
		}
	}

	destDir := filepath.Join(".", "uploads", "music", "covers")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	destPath := filepath.Join(destDir, filename)
	if err := os.WriteFile(destPath, content, 0644); err != nil {
		return "", err
	}
	return "/uploads/music/covers/" + filename, nil
}

func deriveTrackFromArchiveEntry(name string) (string, int, bool) {
	// Filter out system metadata directories like __MACOSX and hidden directories/files starting with . (excluding "." and "..")
	segments := strings.Split(filepath.ToSlash(name), "/")
	for _, segment := range segments {
		if (strings.HasPrefix(segment, ".") && segment != "." && segment != "..") || segment == "__MACOSX" {
			return "", 0, false
		}
	}
	base := filepath.Base(name)
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".mp3", ".flac", ".wav", ".m4a", ".aac", ".ogg":
	default:
		return "", 0, false
	}

	title := strings.TrimSuffix(base, filepath.Ext(base))
	trackNumber := 0
	var parts = strings.SplitN(title, " - ", 2)
	if len(parts) == 2 {
		if value, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
			trackNumber = value
			title = parts[1]
		}
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", 0, false
	}
	return title, trackNumber, true
}
