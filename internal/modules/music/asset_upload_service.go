package music

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/musicmedia"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	musicAssetUploadPartSize = 16 * 1024 * 1024
	musicAssetUploadMaxSize  = 200 * 1024 * 1024
	musicAssetUploadLifetime = 24 * time.Hour

	musicAssetUploadStatusUploading  = "uploading"
	musicAssetUploadStatusCompleting = "completing"
	musicAssetUploadStatusExpiring   = "expiring"
	musicAssetUploadStatusCompleted  = "completed"
	musicAssetUploadStatusCanceled   = "canceled"
	musicAssetUploadStatusFailed     = "failed"
)

type CreateMusicAssetUploadInput struct {
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

type MusicAssetUploadPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
}

type MusicAssetUploadSessionDTO struct {
	ID             string                 `json:"id"`
	Status         string                 `json:"status"`
	FileName       string                 `json:"file_name"`
	ContentType    string                 `json:"content_type"`
	Size           int64                  `json:"size"`
	PartSize       int64                  `json:"part_size"`
	CompletedParts []MusicAssetUploadPart `json:"completed_parts"`
	ExpiresAt      time.Time              `json:"expires_at"`
	Asset          *model.MediaAsset      `json:"asset,omitempty"`
}

type MusicAssetUploadPartURL struct {
	PartNumber int    `json:"part_number"`
	UploadURL  string `json:"upload_url"`
}

func (s *Service) CleanupExpiredMusicAssetUploads(ctx context.Context) error {
	if s.db == nil || s.assetUploadMultipart == nil {
		return nil
	}
	now := time.Now().UTC()
	var candidates []model.MusicAssetUploadSession
	if err := s.db.WithContext(ctx).
		Where("expires_at <= ? AND status IN ?", now, []string{musicAssetUploadStatusUploading, musicAssetUploadStatusCompleting, musicAssetUploadStatusExpiring}).
		Find(&candidates).Error; err != nil {
		return err
	}
	for _, candidate := range candidates {
		var session model.MusicAssetUploadSession
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, "id = ?", candidate.ID).Error; err != nil {
				return err
			}
			if session.ExpiresAt.After(now) || (session.Status != musicAssetUploadStatusUploading && session.Status != musicAssetUploadStatusCompleting && session.Status != musicAssetUploadStatusExpiring) {
				return nil
			}
			session.Status = musicAssetUploadStatusExpiring
			return tx.Save(&session).Error
		}); err != nil {
			return err
		}
		if session.ID == uuid.Nil || session.Status != musicAssetUploadStatusExpiring {
			continue
		}
		if err := s.assetUploadMultipart.AbortMultipartUpload(session.ObjectKey, session.UploadID); err != nil {
			_ = s.db.WithContext(ctx).Model(&model.MusicAssetUploadSession{}).Where("id = ? AND status = ?", session.ID, musicAssetUploadStatusExpiring).
				Update("error_message", truncateMusicAssetUploadError(err.Error())).Error
			continue
		}
		if err := s.db.WithContext(ctx).Model(&model.MusicAssetUploadSession{}).Where("id = ? AND status = ?", session.ID, musicAssetUploadStatusExpiring).
			Updates(map[string]any{"status": musicAssetUploadStatusCanceled, "error_message": ""}).Error; err != nil {
			return err
		}
	}
	return nil
}

func truncateMusicAssetUploadError(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 512 {
		return message[:512]
	}
	return message
}

func (s *Service) CreateMusicAssetUpload(user authctx.CurrentUser, input CreateMusicAssetUploadInput) (MusicAssetUploadSessionDTO, error) {
	if user.ID == uuid.Nil {
		return MusicAssetUploadSessionDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.assetUploadMultipart); err != nil {
		return MusicAssetUploadSessionDTO{}, err
	}
	fileName := strings.TrimSpace(input.FileName)
	contentType := strings.TrimSpace(input.ContentType)
	if fileName == "" || !isMusicAudioContentType(contentType) || input.Size <= 0 {
		return MusicAssetUploadSessionDTO{}, apperr.BadRequest("validation.invalid_request", "audio upload metadata is invalid")
	}
	if input.Size > musicAssetUploadMaxSize {
		return MusicAssetUploadSessionDTO{}, apperr.BadRequest("music.upload_too_large", "Audio file exceeds the 200MB limit")
	}
	urlPrefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
	if urlPrefix == "" {
		return MusicAssetUploadSessionDTO{}, apperr.Internal(errors.New("s3 url prefix is not configured"))
	}

	sessionID := uuid.New()
	key := storage.BuildMusicUploadKey("audio", user.ID.String(), uniqueMusicAssetUploadName(fileName, contentType), time.Now().UTC())
	uploadID, err := s.assetUploadMultipart.CreateMultipartUpload(key, contentType)
	if err != nil {
		return MusicAssetUploadSessionDTO{}, err
	}
	now := time.Now().UTC()
	session := model.MusicAssetUploadSession{
		Base:               model.Base{ID: sessionID},
		UserID:             user.ID,
		Status:             musicAssetUploadStatusUploading,
		FileName:           fileName,
		ContentType:        contentType,
		Size:               input.Size,
		ObjectKey:          key,
		UploadID:           uploadID,
		PartSize:           musicAssetUploadPartSize,
		CompletedPartsJSON: "[]",
		ExpiresAt:          now.Add(musicAssetUploadLifetime),
	}
	if err := s.db.Create(&session).Error; err != nil {
		_ = s.assetUploadMultipart.AbortMultipartUpload(key, uploadID)
		return MusicAssetUploadSessionDTO{}, err
	}
	return buildMusicAssetUploadSessionDTO(session, nil), nil
}

func (s *Service) GetMusicAssetUpload(user authctx.CurrentUser, id uuid.UUID) (MusicAssetUploadSessionDTO, error) {
	session, asset, err := s.loadMusicAssetUpload(user, id)
	if err != nil {
		return MusicAssetUploadSessionDTO{}, err
	}
	return buildMusicAssetUploadSessionDTO(session, asset), nil
}

func (s *Service) CreateMusicAssetUploadPart(user authctx.CurrentUser, id uuid.UUID, partNumber int) (MusicAssetUploadPartURL, error) {
	if partNumber <= 0 {
		return MusicAssetUploadPartURL{}, apperr.BadRequest("validation.invalid_request", "part number is invalid")
	}
	session, _, err := s.loadMusicAssetUpload(user, id)
	if err != nil {
		return MusicAssetUploadPartURL{}, err
	}
	if session.Status != musicAssetUploadStatusUploading || !session.ExpiresAt.After(time.Now().UTC()) {
		return MusicAssetUploadPartURL{}, apperr.Unprocessable("music.upload_invalid_status", "Audio upload is not available")
	}
	if partNumber > musicAssetUploadPartCount(session.Size, session.PartSize) {
		return MusicAssetUploadPartURL{}, apperr.BadRequest("validation.invalid_request", "part number is invalid")
	}
	url, err := s.assetUploadMultipart.PresignUploadPart(session.ObjectKey, session.UploadID, partNumber, 15*time.Minute)
	if err != nil {
		return MusicAssetUploadPartURL{}, err
	}
	return MusicAssetUploadPartURL{PartNumber: partNumber, UploadURL: url}, nil
}

func (s *Service) CompleteMusicAssetUploadPart(user authctx.CurrentUser, id uuid.UUID, partNumber int, part MusicAssetUploadPart) (MusicAssetUploadSessionDTO, error) {
	if partNumber <= 0 || strings.TrimSpace(part.ETag) == "" || part.Size <= 0 {
		return MusicAssetUploadSessionDTO{}, apperr.BadRequest("validation.invalid_request", "completed part is invalid")
	}
	var out model.MusicAssetUploadSession
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var session model.MusicAssetUploadSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error; err != nil {
			return musicAssetUploadNotFound(err)
		}
		if session.Status != musicAssetUploadStatusUploading || !session.ExpiresAt.After(time.Now().UTC()) {
			return apperr.Unprocessable("music.upload_invalid_status", "Audio upload is not available")
		}
		if partNumber > musicAssetUploadPartCount(session.Size, session.PartSize) || part.Size != musicAssetUploadExpectedPartSize(session.Size, session.PartSize, partNumber) {
			return apperr.BadRequest("validation.invalid_request", "completed part size is invalid")
		}
		parts, err := musicAssetUploadParts(session.CompletedPartsJSON)
		if err != nil {
			return err
		}
		part.PartNumber = partNumber
		replaced := false
		for index := range parts {
			if parts[index].PartNumber == partNumber {
				parts[index] = part
				replaced = true
				break
			}
		}
		if !replaced {
			parts = append(parts, part)
		}
		sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
		encoded, err := json.Marshal(parts)
		if err != nil {
			return err
		}
		session.CompletedPartsJSON = string(encoded)
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = session
		return nil
	}); err != nil {
		return MusicAssetUploadSessionDTO{}, err
	}
	return buildMusicAssetUploadSessionDTO(out, nil), nil
}

func musicAssetUploadParts(raw string) ([]MusicAssetUploadPart, error) {
	parts := []MusicAssetUploadPart{}
	if strings.TrimSpace(raw) == "" {
		return parts, nil
	}
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return nil, err
	}
	return parts, nil
}

func musicAssetUploadPartCount(size, partSize int64) int {
	return int((size + partSize - 1) / partSize)
}

func musicAssetUploadExpectedPartSize(size, partSize int64, partNumber int) int64 {
	start := int64(partNumber-1) * partSize
	return min(partSize, size-start)
}

func buildMusicAssetUploadSessionDTO(session model.MusicAssetUploadSession, asset *model.MediaAsset) MusicAssetUploadSessionDTO {
	parts, _ := musicAssetUploadParts(session.CompletedPartsJSON)
	return MusicAssetUploadSessionDTO{ID: session.ID.String(), Status: session.Status, FileName: session.FileName, ContentType: session.ContentType, Size: session.Size, PartSize: session.PartSize, CompletedParts: parts, ExpiresAt: session.ExpiresAt, Asset: asset}
}

func (s *Service) loadMusicAssetUpload(user authctx.CurrentUser, id uuid.UUID) (model.MusicAssetUploadSession, *model.MediaAsset, error) {
	if user.ID == uuid.Nil {
		return model.MusicAssetUploadSession{}, nil, apperr.Unauthorized("Login required")
	}
	var session model.MusicAssetUploadSession
	if err := s.db.Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error; err != nil {
		return model.MusicAssetUploadSession{}, nil, musicAssetUploadNotFound(err)
	}
	var asset *model.MediaAsset
	if session.AssetID != nil {
		var stored model.MediaAsset
		if err := s.db.First(&stored, "id = ?", *session.AssetID).Error; err == nil {
			asset = &stored
		}
	}
	return session, asset, nil
}

func musicAssetUploadNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("music.upload_not_found", "Audio upload not found")
	}
	return err
}

func isMusicAudioContentType(contentType string) bool {
	switch contentType {
	case "audio/aac", "audio/flac", "audio/mpeg", "audio/mp4", "audio/ogg", "audio/wav", "audio/webm", "audio/x-m4a", "audio/x-wav", "audio/vnd.wave", "application/ogg":
		return true
	default:
		return false
	}
}

func uniqueMusicAssetUploadName(fileName, contentType string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" {
		switch contentType {
		case "audio/mpeg":
			ext = ".mp3"
		case "audio/flac":
			ext = ".flac"
		case "audio/wav", "audio/x-wav", "audio/vnd.wave":
			ext = ".wav"
		default:
			ext = ".audio"
		}
	}
	return uuid.NewString() + ext
}

func (s *Service) CompleteMusicAssetUpload(user authctx.CurrentUser, id uuid.UUID) (*model.MediaAsset, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.assetUploadMultipart); err != nil {
		return nil, err
	}
	var session model.MusicAssetUploadSession
	var parts []MusicAssetUploadPart
	var existing *model.MediaAsset
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error; err != nil {
			return musicAssetUploadNotFound(err)
		}
		if session.Status == musicAssetUploadStatusCompleted && session.AssetID != nil {
			var asset model.MediaAsset
			if err := tx.First(&asset, "id = ?", *session.AssetID).Error; err != nil {
				return err
			}
			existing = &asset
			return nil
		}
		if session.Status != musicAssetUploadStatusUploading && session.Status != musicAssetUploadStatusCompleting {
			return apperr.Unprocessable("music.upload_invalid_status", "Audio upload cannot be completed")
		}
		if !session.ExpiresAt.After(time.Now().UTC()) {
			return apperr.Unprocessable("music.upload_expired", "Audio upload has expired")
		}
		var err error
		parts, err = musicAssetUploadParts(session.CompletedPartsJSON)
		if err != nil {
			return err
		}
		if err := validateMusicAssetUploadParts(session.Size, session.PartSize, parts); err != nil {
			return err
		}
		session.Status = musicAssetUploadStatusCompleting
		return tx.Save(&session).Error
	}); err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	storageParts := make([]AlbumImportMultipartPartDTO, 0, len(parts))
	for _, part := range parts {
		storageParts = append(storageParts, AlbumImportMultipartPartDTO{PartNumber: part.PartNumber, ETag: part.ETag, Size: part.Size})
	}
	size, err := s.assetUploadMultipart.ObjectSize(session.ObjectKey)
	if err != nil {
		if err := s.assetUploadMultipart.CompleteMultipartUpload(session.ObjectKey, session.UploadID, storageParts); err != nil {
			s.failMusicAssetUpload(session, err, false)
			return nil, err
		}
		size, err = s.assetUploadMultipart.ObjectSize(session.ObjectKey)
		if err != nil {
			s.failMusicAssetUpload(session, err, true)
			return nil, err
		}
	}
	if size != session.Size {
		err := apperr.Unprocessable("music.upload_size_mismatch", "Uploaded audio size does not match")
		s.failMusicAssetUpload(session, err, true)
		return nil, err
	}
	object, err := s.assetUploadMultipart.OpenObject(session.ObjectKey)
	if err != nil {
		s.failMusicAssetUpload(session, err, true)
		return nil, err
	}
	matchesAudio := musicmedia.MatchesDeclaredAudio(object, session.ContentType)
	closeErr := object.Close()
	if closeErr != nil {
		s.failMusicAssetUpload(session, closeErr, true)
		return nil, closeErr
	}
	if !matchesAudio {
		err := apperr.Unprocessable("music.upload_content_type_mismatch", "Uploaded file content is not valid audio")
		s.failMusicAssetUpload(session, err, true)
		return nil, err
	}
	urlPrefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
	if urlPrefix == "" {
		err := apperr.Internal(errors.New("s3 url prefix is not configured"))
		s.failMusicAssetUpload(session, err, true)
		return nil, err
	}

	var asset model.MediaAsset
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var locked model.MusicAssetUploadSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, user.ID).First(&locked).Error; err != nil {
			return musicAssetUploadNotFound(err)
		}
		if locked.Status == musicAssetUploadStatusCompleted && locked.AssetID != nil {
			return tx.First(&asset, "id = ?", *locked.AssetID).Error
		}
		if locked.Status != musicAssetUploadStatusCompleting {
			return apperr.Unprocessable("music.upload_invalid_status", "Audio upload cannot be completed")
		}
		asset = model.MediaAsset{UserID: &user.ID, Purpose: "music.audio", URL: urlPrefix + "/" + locked.ObjectKey, Key: locked.ObjectKey, ContentType: locked.ContentType, Size: locked.Size}
		if err := tx.Create(&asset).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		locked.Status = musicAssetUploadStatusCompleted
		locked.CompletedAt = &now
		locked.AssetID = &asset.ID
		return tx.Save(&locked).Error
	}); err != nil {
		s.failMusicAssetUpload(session, err, true)
		return nil, err
	}
	return &asset, nil
}

func (s *Service) failMusicAssetUpload(session model.MusicAssetUploadSession, cause error, deleteObject bool) {
	if err := s.assetUploadMultipart.AbortMultipartUpload(session.ObjectKey, session.UploadID); err != nil {
		log.Printf("abort failed music asset upload %s: %v", session.ID, err)
	}
	if deleteObject {
		if err := s.assetUploadMultipart.DeleteObject(session.ObjectKey); err != nil {
			log.Printf("delete failed music asset upload object %s: %v", session.ID, err)
		}
	}
	if err := s.db.Model(&model.MusicAssetUploadSession{}).
		Where("id = ? AND status = ?", session.ID, musicAssetUploadStatusCompleting).
		Updates(map[string]any{"status": musicAssetUploadStatusFailed, "error_message": truncateMusicAssetUploadError(cause.Error())}).Error; err != nil {
		log.Printf("mark failed music asset upload %s: %v", session.ID, err)
	}
}

func validateMusicAssetUploadParts(size, partSize int64, parts []MusicAssetUploadPart) error {
	totalParts := musicAssetUploadPartCount(size, partSize)
	if len(parts) != totalParts {
		return apperr.Unprocessable("music.upload_incomplete", "Audio upload is incomplete")
	}
	for index, part := range parts {
		partNumber := index + 1
		if part.PartNumber != partNumber || strings.TrimSpace(part.ETag) == "" || part.Size != musicAssetUploadExpectedPartSize(size, partSize, partNumber) {
			return apperr.Unprocessable("music.upload_incomplete", "Audio upload is incomplete")
		}
	}
	return nil
}

func (s *Service) CancelMusicAssetUpload(user authctx.CurrentUser, id uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.assetUploadMultipart); err != nil {
		return err
	}
	var session model.MusicAssetUploadSession
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error; err != nil {
			return musicAssetUploadNotFound(err)
		}
		if session.Status == musicAssetUploadStatusCompleted {
			return apperr.Unprocessable("music.upload_invalid_status", "Completed audio upload cannot be canceled")
		}
		session.Status = musicAssetUploadStatusCanceled
		return tx.Save(&session).Error
	}); err != nil {
		return err
	}
	return s.assetUploadMultipart.AbortMultipartUpload(session.ObjectKey, session.UploadID)
}
