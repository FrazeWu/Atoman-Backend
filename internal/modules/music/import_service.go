package music

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var albumImportCreateAlbumHook func(*gorm.DB, *model.Album) error

func buildAlbumImportDTO(session model.AlbumImportSession) AlbumImportDTO {
	payload := map[string]any{}
	if strings.TrimSpace(session.PayloadJSON) != "" {
		_ = json.Unmarshal([]byte(session.PayloadJSON), &payload)
	}

	dto := AlbumImportDTO{
		ImportID:       session.ID.String(),
		Status:         session.Status,
		ArchiveName:    stringValue(payload["archive_name"]),
		UploadProgress: floatValue(payload["upload_progress"]),
		UploadSpeed:    floatValue(payload["upload_speed"]),
		CoverURL:       stringValue(payload["cover_url"]),
		CoverKey:       stringValue(payload["cover_key"]),
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
	payloadPatch, err := deriveAlbumImportPayload(strings.TrimSpace(archiveName), body)
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
		if strings.TrimSpace(payload.Artist.Name) == "" {
			return apperr.BadRequest("validation.invalid_request", "artist name is required")
		}
		if strings.TrimSpace(payload.Album.Title) == "" {
			return apperr.BadRequest("validation.invalid_request", "album title is required")
		}

		artist := model.Artist{
			Name:           strings.TrimSpace(payload.Artist.Name),
			LegalName:      strings.TrimSpace(payload.Artist.LegalName),
			StageNamesJSON: mustMarshalStageNames(payload.Artist.StageNames),
			BirthPlace:     strings.TrimSpace(payload.Artist.BirthPlace),
			EntryStatus:    "open",
		}
		if err := createAlbumImportArtist(tx, &artist); err != nil {
			return err
		}

		album := model.Album{
			Title:       strings.TrimSpace(payload.Album.Title),
			ReleaseYear: payload.Album.ReleaseYear,
			Year:        payload.Album.ReleaseYear,
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

func deriveAlbumImportPayload(archiveName string, body []byte) (map[string]any, error) {
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", "archive must be a valid zip file")
	}

	derivedTracks := make([]map[string]any, 0)
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		trackTitle, trackNumber, ok := deriveTrackFromArchiveEntry(file.Name)
		if !ok {
			continue
		}
		derivedTracks = append(derivedTracks, map[string]any{
			"title":        trackTitle,
			"track_number": trackNumber,
			"audio_key":    "",
			"origin":       file.Name,
		})
	}

	return map[string]any{
		"archive_name":        archiveName,
		"derived_album_title": strings.TrimSpace(strings.TrimSuffix(archiveName, filepath.Ext(archiveName))),
		"derived_tracks":      derivedTracks,
		"upload_progress":     100,
		"upload_speed":        0,
	}, nil
}

func deriveTrackFromArchiveEntry(name string) (string, int, bool) {
	// Filter out system metadata directories like __MACOSX and hidden directories/files starting with .
	segments := strings.Split(filepath.ToSlash(name), "/")
	for _, segment := range segments {
		if strings.HasPrefix(segment, ".") || segment == "__MACOSX" {
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
