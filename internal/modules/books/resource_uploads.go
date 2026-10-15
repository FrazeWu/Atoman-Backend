package books

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/storage"

	"github.com/google/uuid"
	"golang.org/x/text/language"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CreateBookResourceInput struct {
	Title    string `json:"title"`
	Language string `json:"language"`
	Format   string `json:"format"`
}

type CreateBookResourceUploadInput struct {
	FileName                    string `json:"file_name"`
	ContentType                 string `json:"content_type"`
	Size                        int64  `json:"size"`
	PublicDistributionConfirmed bool   `json:"public_distribution_confirmed"`
}

type BookResourceUploadDTO struct {
	model.BookUploadSession
	CompletedParts []BookUploadPart `json:"completed_parts"`
}

func bookResourceUploadDTO(session model.BookUploadSession) BookResourceUploadDTO {
	parts, _ := bookUploadParts(session.CompletedPartsJSON)
	return BookResourceUploadDTO{BookUploadSession: session, CompletedParts: parts}
}

func normalizeBookResourceLanguage(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "und", nil
	}
	tag, err := language.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", apperr.BadRequest("books.invalid_language", "Use a language code such as zh or en")
	}
	return tag.String(), nil
}

func (s *Service) CreateBookResource(ctx context.Context, user authctx.CurrentUser, workID uuid.UUID, input CreateBookResourceInput) (model.BookAsset, error) {
	if user.ID == uuid.Nil {
		return model.BookAsset{}, apperr.Unauthorized("Login required")
	}
	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format != "epub" && format != "pdf" && format != "txt" && format != "cbz" && format != "cbr" {
		return model.BookAsset{}, apperr.BadRequest("books.invalid_format", "Unsupported ebook format")
	}
	lang, err := normalizeBookResourceLanguage(input.Language)
	if err != nil {
		return model.BookAsset{}, err
	}
	asset := model.BookAsset{WorkID: workID, Title: strings.TrimSpace(input.Title), Language: lang, Format: format, CreatedBy: user.ID}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var work model.BookWork
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND lifecycle_status = ? AND edit_status <> ?", workID, model.BookLifecycleStatusActive, model.BookEditStatusClosed).First(&work).Error; err != nil {
			return publicBookNotFound(err)
		}
		if _, err := currentBookWikiRevision(tx, "work", workID); err != nil {
			return publicBookNotFound(err)
		}
		return tx.Create(&asset).Error
	})
	return asset, err
}

func (s *Service) ListBookResources(ctx context.Context, workID uuid.UUID) ([]model.BookAsset, error) {
	work, err := s.GetBookWork(ctx, workID)
	if err != nil {
		return nil, err
	}
	workID = uuid.MustParse(work.ID)
	assets := make([]model.BookAsset, 0)
	err = s.db.WithContext(ctx).Joins("JOIN book_asset_revisions r ON r.id = book_assets.current_revision_id AND r.deleted_at IS NULL AND r.status = ?", model.BookRevisionStatusReady).
		Where("book_assets.work_id = ? AND book_assets.status = ?", workID, model.BookResourceStatusActive).Order("book_assets.created_at ASC").Find(&assets).Error
	return assets, err
}

func (s *Service) loadBookResourceUpload(ctx context.Context, user authctx.CurrentUser, id uuid.UUID) (model.BookUploadSession, error) {
	if user.ID == uuid.Nil {
		return model.BookUploadSession{}, apperr.Unauthorized("Login required")
	}
	var session model.BookUploadSession
	err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return session, apperr.NotFound("books.upload_not_found", "Upload session not found")
	}
	return session, err
}

func (s *Service) GetBookResourceUpload(ctx context.Context, user authctx.CurrentUser, id uuid.UUID) (BookResourceUploadDTO, error) {
	session, err := s.loadBookResourceUpload(ctx, user, id)
	return bookResourceUploadDTO(session), err
}

func (s *Service) CreateBookResourceUpload(ctx context.Context, user authctx.CurrentUser, assetID uuid.UUID, input CreateBookResourceUploadInput) (BookResourceUploadDTO, error) {
	if user.ID == uuid.Nil {
		return BookResourceUploadDTO{}, apperr.Unauthorized("Login required")
	}
	if !input.PublicDistributionConfirmed {
		return BookResourceUploadDTO{}, apperr.BadRequest("books.public_distribution_confirmation_required", "Confirm permission to share this file publicly")
	}
	format, err := validateBookUploadMetadata(input.FileName, input.ContentType, input.Size)
	if err != nil {
		return BookResourceUploadDTO{}, err
	}
	if err := requireBookUploadStore(s.bookResources); err != nil {
		return BookResourceUploadDTO{}, err
	}
	var asset model.BookAsset
	if err := s.db.WithContext(ctx).Where("id = ? AND status = ?", assetID, model.BookResourceStatusActive).First(&asset).Error; err != nil {
		return BookResourceUploadDTO{}, publicBookNotFound(err)
	}
	if asset.Format != format {
		return BookResourceUploadDTO{}, apperr.BadRequest("books.format_mismatch", "Replacement must use the existing ebook's format; create a separate asset for another format")
	}
	var active int64
	if err := s.db.WithContext(ctx).Model(&model.BookUploadSession{}).Where("user_id = ? AND status IN ? AND expires_at > ?", user.ID, []string{"uploading", "completing", "uploaded", "scanning"}, time.Now().UTC()).Count(&active).Error; err != nil {
		return BookResourceUploadDTO{}, err
	}
	if active >= bookUploadMaxActiveSessions {
		return BookResourceUploadDTO{}, apperr.Conflict("books.upload_quota_exceeded", "Too many active uploads")
	}
	id := uuid.New()
	key := storage.BuildBookRevisionObjectKey(id.String(), format)
	uploadID, err := s.bookResources.CreateMultipartUpload(key, input.ContentType)
	if err != nil {
		return BookResourceUploadDTO{}, apperr.Wrap(http.StatusServiceUnavailable, "storage.unavailable", "Book storage is unavailable", err)
	}
	session := model.BookUploadSession{Base: model.Base{ID: id}, UserID: user.ID, AssetID: assetID, OriginalFilename: strings.TrimSpace(input.FileName), Format: format, ContentType: input.ContentType, SizeBytes: input.Size, ObjectKey: key, UploadID: uploadID, PartSize: bookUploadPartSize, CompletedPartsJSON: "[]", ExpiresAt: time.Now().UTC().Add(bookUploadLifetime), Status: "uploading", PublicDistributionConfirmed: true}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var work model.BookWork
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND lifecycle_status = ? AND edit_status <> ?", asset.WorkID, model.BookLifecycleStatusActive, model.BookEditStatusClosed).First(&work).Error; err != nil {
			return publicBookNotFound(err)
		}
		return tx.Create(&session).Error
	})
	if err != nil {
		_ = s.bookResources.AbortMultipartUpload(key, uploadID)
	}
	return bookResourceUploadDTO(session), err
}

func validateBookResourceUploading(session model.BookUploadSession) error {
	if session.Status != "uploading" || !session.ExpiresAt.After(time.Now().UTC()) {
		return apperr.Unprocessable("books.upload_invalid_status", "Upload is no longer available")
	}
	return nil
}

func (s *Service) CreateBookResourceUploadPart(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, number int) (BookUploadPartURL, error) {
	session, err := s.loadBookResourceUpload(ctx, user, id)
	if err != nil {
		return BookUploadPartURL{}, err
	}
	if err := validateBookResourceUploading(session); err != nil {
		return BookUploadPartURL{}, err
	}
	if number < 1 || number > bookUploadPartCount(session.SizeBytes, session.PartSize) || number > bookUploadMaxParts {
		return BookUploadPartURL{}, apperr.BadRequest("books.invalid_part", "Invalid upload part")
	}
	if err := requireBookUploadStore(s.bookResources); err != nil {
		return BookUploadPartURL{}, err
	}
	url, err := s.bookResources.PresignUploadPart(session.ObjectKey, session.UploadID, number, bookUploadPartURLLifetime)
	return BookUploadPartURL{PartNumber: number, UploadURL: url}, err
}

func (s *Service) CompleteBookResourceUploadPart(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, number int, part BookUploadPart) (BookResourceUploadDTO, error) {
	if user.ID == uuid.Nil {
		return BookResourceUploadDTO{}, apperr.Unauthorized("Login required")
	}
	var session model.BookUploadSession
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error; err != nil {
			return publicBookNotFound(err)
		}
		if err := validateBookResourceUploading(session); err != nil {
			return err
		}
		if number < 1 || number > bookUploadPartCount(session.SizeBytes, session.PartSize) || part.Size != bookUploadExpectedPartSize(session.SizeBytes, session.PartSize, number) || strings.TrimSpace(part.ETag) == "" || len(part.ETag) > 256 {
			return apperr.BadRequest("books.invalid_part", "Invalid upload part")
		}
		parts, err := bookUploadParts(session.CompletedPartsJSON)
		if err != nil {
			return err
		}
		part.PartNumber = number
		replaced := false
		for i := range parts {
			if parts[i].PartNumber == number {
				parts[i] = part
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
		return tx.Model(&session).Update("completed_parts_json", session.CompletedPartsJSON).Error
	})
	return bookResourceUploadDTO(session), err
}

func (s *Service) CompleteBookResourceUpload(ctx context.Context, user authctx.CurrentUser, id uuid.UUID) (BookResourceUploadDTO, error) {
	if user.ID == uuid.Nil {
		return BookResourceUploadDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireBookUploadStore(s.bookResources); err != nil {
		return BookResourceUploadDTO{}, err
	}
	var session model.BookUploadSession
	var parts []BookUploadPart
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error; err != nil {
			return publicBookNotFound(err)
		}
		if session.Status == "uploaded" || session.Status == "scanning" || session.Status == "ready" {
			return nil
		}
		if err := validateBookResourceUploading(session); err != nil {
			return err
		}
		var err error
		parts, err = bookUploadParts(session.CompletedPartsJSON)
		if err != nil {
			return err
		}
		if err := validateBookUploadParts(session.SizeBytes, session.PartSize, parts); err != nil {
			return err
		}
		session.Status = "completing"
		return tx.Model(&session).Update("status", session.Status).Error
	})
	if err != nil || session.Status != "completing" {
		return bookResourceUploadDTO(session), err
	}
	completeErr := s.bookResources.CompleteMultipartUpload(session.ObjectKey, session.UploadID, parts)
	size, sizeErr := s.bookResources.ObjectSize(session.ObjectKey)
	if sizeErr != nil || size != session.SizeBytes {
		cause := completeErr
		if cause == nil {
			cause = sizeErr
		}
		if cause == nil {
			cause = errors.New("uploaded file size does not match")
		}
		_ = s.db.WithContext(ctx).Model(&session).Update("status", "uploading").Error
		return BookResourceUploadDTO{}, apperr.Wrap(http.StatusServiceUnavailable, "books.upload_completion_failed", "Upload could not be verified; retry completion", cause)
	}
	// An object of the exact expected size also handles a lost S3 completion
	// response. Structural validation and antivirus still run before publishing.
	session.Status = "uploaded"
	err = s.db.WithContext(ctx).Model(&session).Update("status", session.Status).Error
	return bookResourceUploadDTO(session), err
}
