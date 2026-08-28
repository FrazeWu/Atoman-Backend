package books

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const bookRightsDeclarationMaxLength = 20000

type SubmitPublicationInput struct {
	WorkID       string `json:"work_id"`
	EditionID    string `json:"edition_id"`
	LicenseType  string `json:"license_type"`
	RightsHolder string `json:"rights_holder"`
	SourceURL    string `json:"source_url"`
	Declaration  string `json:"declaration"`
	Reason       string `json:"reason"`
}

type BookPublicationRequestDTO struct {
	ID                   string     `json:"id"`
	AssetID              string     `json:"asset_id"`
	WorkID               string     `json:"work_id,omitempty"`
	EditionID            string     `json:"edition_id,omitempty"`
	Status               string     `json:"status"`
	LicenseType          string     `json:"license_type"`
	RightsHolder         string     `json:"rights_holder"`
	SourceURL            string     `json:"source_url"`
	Declaration          string     `json:"declaration"`
	EvidenceUploaded     bool       `json:"evidence_uploaded"`
	PublishedAssetStatus string     `json:"published_asset_status,omitempty"`
	Reason               string     `json:"reason,omitempty"`
	DecisionNote         string     `json:"decision_note,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	ReviewedAt           *time.Time `json:"reviewed_at,omitempty"`
}

type BookPublicationRequestListResult struct {
	Items  []BookPublicationRequestDTO `json:"items"`
	Total  int64                       `json:"total"`
	Limit  int                         `json:"limit"`
	Offset int                         `json:"offset"`
}

type BookPublishedAssetDTO struct {
	ID          string    `json:"id"`
	WorkID      string    `json:"work_id,omitempty"`
	EditionID   string    `json:"edition_id,omitempty"`
	Format      string    `json:"format"`
	FileName    string    `json:"file_name"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

type reviewPublicationInput struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

func (s *Service) SubmitPublicationRequest(user authctx.CurrentUser, assetID uuid.UUID, input SubmitPublicationInput) (BookPublicationRequestDTO, error) {
	if user.ID == uuid.Nil {
		return BookPublicationRequestDTO{}, apperr.Unauthorized("Login required")
	}
	if err := validatePublicationInput(input); err != nil {
		return BookPublicationRequestDTO{}, err
	}
	asset, bookImport, err := s.loadBookAssetOwner(user, assetID)
	if err != nil {
		return BookPublicationRequestDTO{}, err
	}
	if !isPrivateReadableAssetStatus(asset.ProcessingStatus) || bookImport.Status != model.BookImportStatusMetadataReady {
		return BookPublicationRequestDTO{}, apperr.Unprocessable("books.asset_not_ready", "Book asset is not ready for publication")
	}
	workID, editionID, err := parsePublicationTargets(input)
	if err != nil {
		return BookPublicationRequestDTO{}, err
	}
	if workID != nil {
		if _, err := s.GetPublicWork(context.Background(), *workID); err != nil {
			return BookPublicationRequestDTO{}, err
		}
	}
	if editionID != nil {
		edition, err := s.GetPublicEdition(context.Background(), *editionID)
		if err != nil {
			return BookPublicationRequestDTO{}, err
		}
		if workID != nil && edition.Edition.WorkID != workID.String() {
			return BookPublicationRequestDTO{}, apperr.BadRequest("validation.invalid_request", "edition does not belong to work")
		}
	}
	var existing model.BookPublicationRequest
	if err := s.db.Where("asset_id = ? AND status = ?", asset.ID, model.BookPublicationStatusPendingReview).First(&existing).Error; err == nil {
		return BookPublicationRequestDTO{}, apperr.Conflict("books.publication_already_requested", "Publication request is already pending")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return BookPublicationRequestDTO{}, err
	}
	var request model.BookPublicationRequest
	err = s.db.Transaction(func(tx *gorm.DB) error {
		request = model.BookPublicationRequest{SubmittedBy: user.ID, AssetID: asset.ID, WorkID: workID, EditionID: editionID, Status: model.BookPublicationStatusPendingReview, Reason: strings.TrimSpace(input.Reason)}
		if err := tx.Create(&request).Error; err != nil {
			return err
		}
		rights := model.BookRightsDeclaration{RequestID: request.ID, LicenseType: input.LicenseType, RightsHolder: strings.TrimSpace(input.RightsHolder), SourceURL: strings.TrimSpace(input.SourceURL), Declaration: strings.TrimSpace(input.Declaration)}
		if err := tx.Create(&rights).Error; err != nil {
			return err
		}
		return tx.Model(&model.UserBookAsset{}).Where("id = ? AND processing_status = ?", asset.ID, asset.ProcessingStatus).Updates(map[string]any{"processing_status": model.BookAssetStatusPublicationRequested}).Error
	})
	if err != nil {
		return BookPublicationRequestDTO{}, err
	}
	return buildBookPublicationRequestDTO(s.db, request), nil
}

func (s *Service) ListPublicationRequests(ctx context.Context, user authctx.CurrentUser, reviewQueue bool, limit, offset int) ([]BookPublicationRequestDTO, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	if reviewQueue && !CanReviewSubmission(BookViewer{UserID: user.ID, Role: user.Role}, uuid.New()) {
		return nil, 0, apperr.Forbidden("books.review_forbidden", "Book review permission is required")
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Model(&model.BookPublicationRequest{})
	if reviewQueue {
		query = query.Where("status = ? AND submitted_by <> ?", model.BookPublicationStatusPendingReview, user.ID)
	} else {
		query = query.Where("submitted_by = ?", user.ID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var requests []model.BookPublicationRequest
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&requests).Error; err != nil {
		return nil, 0, err
	}
	result := make([]BookPublicationRequestDTO, 0, len(requests))
	for _, request := range requests {
		result = append(result, buildBookPublicationRequestDTO(s.db, request))
	}
	return result, total, nil
}

func (s *Service) ReviewPublicationRequest(reviewer authctx.CurrentUser, requestID uuid.UUID, decision, note string) (BookPublicationRequestDTO, error) {
	if reviewer.ID == uuid.Nil {
		return BookPublicationRequestDTO{}, apperr.Unauthorized("Login required")
	}
	if !CanReviewSubmission(BookViewer{UserID: reviewer.ID, Role: reviewer.Role}, uuid.New()) {
		return BookPublicationRequestDTO{}, apperr.Forbidden("books.review_forbidden", "Book review permission is required")
	}
	if decision != model.BookPublicationStatusPublished && decision != model.BookPublicationStatusRejected && decision != model.BookPublicationStatusQuarantined {
		return BookPublicationRequestDTO{}, apperr.BadRequest("validation.invalid_request", "publication decision is invalid")
	}
	var request model.BookPublicationRequest
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", requestID).First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("books.publication_request_not_found", "Publication request not found")
			}
			return err
		}
		if request.SubmittedBy == reviewer.ID {
			return apperr.Forbidden("books.self_review_forbidden", "You cannot review your own publication request")
		}
		if request.Status != model.BookPublicationStatusPendingReview {
			return apperr.Conflict("books.publication_already_reviewed", "Publication request is no longer pending")
		}
		if decision == model.BookPublicationStatusPublished {
			var source model.UserBookAsset
			if err := tx.Where("id = ? AND user_id = ?", request.AssetID, request.SubmittedBy).First(&source).Error; err != nil {
				return bookAssetNotFound(err)
			}
			published := model.PublishedBookAsset{PublicationRequestID: request.ID, SourceAssetID: source.ID, WorkID: request.WorkID, EditionID: request.EditionID, Format: source.Format, ObjectKey: "pending", SHA256: source.SHA256, Status: model.BookPublicationStatusPendingReview}
			if err := tx.Create(&published).Error; err != nil {
				return err
			}
			publicKey := storage.BuildBookPublishedObjectKey(published.ID.String(), source.Format)
			if s.bookUpload == nil {
				return apperr.New(http.StatusServiceUnavailable, "storage.unavailable", "Storage is unavailable", nil)
			}
			if err := s.bookUpload.CopyObject(source.ObjectKey, publicKey, source.ContentType); err != nil {
				return apperr.Wrap(http.StatusServiceUnavailable, "storage.unavailable", "Storage is unavailable", err)
			}
			if err := tx.Model(&published).Updates(map[string]any{"object_key": publicKey, "status": model.BookPublicationStatusPublished}).Error; err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		request.Status = decision
		request.ReviewerID = &reviewer.ID
		request.ReviewedAt = &now
		request.DecisionNote = strings.TrimSpace(note)
		if err := tx.Save(&request).Error; err != nil {
			return err
		}
		if decision != model.BookPublicationStatusPublished {
			return tx.Model(&model.UserBookAsset{}).Where("id = ? AND processing_status IN ?", request.AssetID, []string{model.BookAssetStatusPublicationRequested, model.BookAssetStatusPendingReview}).Updates(map[string]any{"processing_status": model.BookAssetStatusPrivateAvailable}).Error
		}
		return tx.Model(&model.UserBookAsset{}).Where("id = ? AND processing_status IN ?", request.AssetID, []string{model.BookAssetStatusPublicationRequested, model.BookAssetStatusPendingReview}).Updates(map[string]any{"processing_status": model.BookAssetStatusPrivateAvailable}).Error
	})
	if err != nil {
		return BookPublicationRequestDTO{}, err
	}
	return buildBookPublicationRequestDTO(s.db, request), nil
}

func (s *Service) ListPublishedBookAssets(ctx context.Context, workID, editionID uuid.UUID, limit, offset int) ([]BookPublishedAssetDTO, int64, error) {
	if workID == uuid.Nil && editionID == uuid.Nil {
		return nil, 0, apperr.BadRequest("validation.invalid_request", "work_id or edition_id is required")
	}
	if workID != uuid.Nil {
		if _, err := s.GetPublicWork(ctx, workID); err != nil {
			return nil, 0, err
		}
	} else if _, err := s.GetPublicEdition(ctx, editionID); err != nil {
		return nil, 0, err
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Model(&model.PublishedBookAsset{}).Where("status = ?", model.BookPublicationStatusPublished)
	if workID != uuid.Nil {
		query = query.Where("work_id = ?", workID)
	}
	if editionID != uuid.Nil {
		query = query.Where("edition_id = ?", editionID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var assets []model.PublishedBookAsset
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&assets).Error; err != nil {
		return nil, 0, err
	}
	result := make([]BookPublishedAssetDTO, 0, len(assets))
	for _, asset := range assets {
		dto, err := s.buildPublishedAssetDTO(ctx, asset)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, dto)
	}
	return result, total, nil
}

func (s *Service) GetPublishedBookAsset(ctx context.Context, assetID uuid.UUID) (BookPublishedAssetDTO, error) {
	var asset model.PublishedBookAsset
	if err := s.db.WithContext(ctx).Where("id = ? AND status = ?", assetID, model.BookPublicationStatusPublished).First(&asset).Error; err != nil {
		return BookPublishedAssetDTO{}, apperr.NotFound("books.published_asset_not_found", "Published book asset not found")
	}
	if asset.WorkID != nil {
		if _, err := s.GetPublicWork(ctx, *asset.WorkID); err != nil {
			return BookPublishedAssetDTO{}, err
		}
	} else if asset.EditionID != nil {
		if _, err := s.GetPublicEdition(ctx, *asset.EditionID); err != nil {
			return BookPublishedAssetDTO{}, err
		}
	}
	return s.buildPublishedAssetDTO(ctx, asset)
}
func (s *Service) OpenPublishedBookAsset(ctx context.Context, assetID uuid.UUID) (BookPublishedAssetDTO, io.ReadCloser, error) {
	var asset model.PublishedBookAsset
	if err := s.db.WithContext(ctx).Where("id = ? AND status = ?", assetID, model.BookPublicationStatusPublished).First(&asset).Error; err != nil {
		return BookPublishedAssetDTO{}, nil, apperr.NotFound("books.published_asset_not_found", "Published book asset not found")
	}
	if asset.WorkID != nil {
		if _, err := s.GetPublicWork(ctx, *asset.WorkID); err != nil {
			return BookPublishedAssetDTO{}, nil, err
		}
	} else if asset.EditionID != nil {
		if _, err := s.GetPublicEdition(ctx, *asset.EditionID); err != nil {
			return BookPublishedAssetDTO{}, nil, err
		}
	}
	var source model.UserBookAsset
	if err := s.db.WithContext(ctx).Where("id = ?", asset.SourceAssetID).First(&source).Error; err != nil {
		return BookPublishedAssetDTO{}, nil, apperr.NotFound("books.published_asset_not_found", "Published book asset not found")
	}
	if s.bookUpload == nil {
		return BookPublishedAssetDTO{}, nil, apperr.New(http.StatusServiceUnavailable, "storage.unavailable", "Storage is unavailable", nil)
	}
	body, err := s.bookUpload.OpenObject(asset.ObjectKey)
	if err != nil {
		return BookPublishedAssetDTO{}, nil, apperr.Wrap(http.StatusServiceUnavailable, "storage.unavailable", "Storage is unavailable", err)
	}
	dto := buildPublishedBookAssetDTO(asset, source)
	return dto, body, nil
}

func (s *Service) SetPublishedBookAssetStatus(reviewer authctx.CurrentUser, assetID uuid.UUID, status, reason string) error {
	if reviewer.ID == uuid.Nil || !authctx.RoleAtLeast(reviewer.Role, authctx.RoleModerator) {
		return apperr.Forbidden("books.review_forbidden", "Book review permission is required")
	}
	if status != model.BookPublicationStatusPublished && status != model.BookPublicationStatusRemoved && status != model.BookPublicationStatusQuarantined {
		return apperr.BadRequest("validation.invalid_request", "published asset status is invalid")
	}
	var asset model.PublishedBookAsset
	if err := s.db.First(&asset, "id = ?", assetID).Error; err != nil {
		return apperr.NotFound("books.published_asset_not_found", "Published book asset not found")
	}
	updates := map[string]any{"status": status, "removal_reason": strings.TrimSpace(reason)}
	if status == model.BookPublicationStatusRemoved {
		now := time.Now().UTC()
		updates["removed_at"] = &now
	} else {
		updates["removed_at"] = nil
	}
	return s.db.Model(&asset).Updates(updates).Error
}

func validatePublicationInput(input SubmitPublicationInput) error {
	switch input.LicenseType {
	case "public_domain", "open_license", "creator_owned", "authorized_distribution":
	default:
		return apperr.BadRequest("validation.invalid_request", "license_type is invalid")
	}
	if strings.TrimSpace(input.Declaration) == "" || len(input.Declaration) > bookRightsDeclarationMaxLength {
		return apperr.BadRequest("validation.invalid_request", "rights declaration is invalid")
	}
	if strings.TrimSpace(input.SourceURL) == "" {
		return apperr.BadRequest("books.source_required", "rights source URL is required")
	}
	return nil
}

func parsePublicationTargets(input SubmitPublicationInput) (*uuid.UUID, *uuid.UUID, error) {
	var workID, editionID *uuid.UUID
	if strings.TrimSpace(input.WorkID) != "" {
		id, err := uuid.Parse(input.WorkID)
		if err != nil {
			return nil, nil, apperr.BadRequest("validation.invalid_request", "work_id is invalid")
		}
		workID = &id
	}
	if strings.TrimSpace(input.EditionID) != "" {
		id, err := uuid.Parse(input.EditionID)
		if err != nil {
			return nil, nil, apperr.BadRequest("validation.invalid_request", "edition_id is invalid")
		}
		editionID = &id
	}
	if workID == nil && editionID == nil {
		return nil, nil, apperr.BadRequest("validation.invalid_request", "work_id or edition_id is required")
	}
	return workID, editionID, nil
}

func isPrivateReadableAssetStatus(status string) bool {
	switch status {
	case model.BookAssetStatusPrivateAvailable, model.BookAssetStatusPublicationRequested, model.BookAssetStatusPendingReview, model.BookAssetStatusRejected:
		return true
	default:
		return false
	}
}

func buildBookPublicationRequestDTO(db *gorm.DB, request model.BookPublicationRequest) BookPublicationRequestDTO {
	dto := BookPublicationRequestDTO{ID: request.ID.String(), AssetID: request.AssetID.String(), Status: request.Status, Reason: request.Reason, DecisionNote: request.DecisionNote, CreatedAt: request.CreatedAt, ReviewedAt: request.ReviewedAt}
	if request.WorkID != nil {
		dto.WorkID = request.WorkID.String()
	}
	if request.EditionID != nil {
		dto.EditionID = request.EditionID.String()
	}
	var rights model.BookRightsDeclaration
	if db.Where("request_id = ?", request.ID).First(&rights).Error == nil {
		dto.LicenseType, dto.RightsHolder, dto.SourceURL, dto.Declaration = rights.LicenseType, rights.RightsHolder, rights.SourceURL, rights.Declaration
		dto.EvidenceUploaded = strings.TrimSpace(rights.EvidenceObjectKey) != ""
	}
	var published model.PublishedBookAsset
	if db.Where("publication_request_id = ?", request.ID).First(&published).Error == nil {
		dto.PublishedAssetStatus = published.Status
	}
	return dto
}

func (s *Service) buildPublishedAssetDTO(ctx context.Context, asset model.PublishedBookAsset) (BookPublishedAssetDTO, error) {
	var source model.UserBookAsset
	if err := s.db.WithContext(ctx).Where("id = ?", asset.SourceAssetID).First(&source).Error; err != nil {
		return BookPublishedAssetDTO{}, apperr.NotFound("books.published_asset_not_found", "Published book asset not found")
	}
	return buildPublishedBookAssetDTO(asset, source), nil
}

func buildPublishedBookAssetDTO(asset model.PublishedBookAsset, source model.UserBookAsset) BookPublishedAssetDTO {
	dto := BookPublishedAssetDTO{ID: asset.ID.String(), Format: asset.Format, FileName: source.OriginalFilename, ContentType: source.ContentType, Size: source.SizeBytes, Status: asset.Status, CreatedAt: asset.CreatedAt}
	if asset.WorkID != nil {
		dto.WorkID = asset.WorkID.String()
	}
	if asset.EditionID != nil {
		dto.EditionID = asset.EditionID.String()
	}
	return dto
}
