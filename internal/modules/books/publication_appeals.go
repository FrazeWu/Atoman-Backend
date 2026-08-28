package books

import (
	"context"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const bookPublicationAppealReasonMaxLength = 5000

type BookPublicationAppealDTO struct {
	ID                   string     `json:"id"`
	PublicationRequestID string     `json:"publication_request_id"`
	PublishedAssetID     string     `json:"published_asset_id"`
	Reason               string     `json:"reason"`
	Status               string     `json:"status"`
	DecisionNote         string     `json:"decision_note,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	ReviewedAt           *time.Time `json:"reviewed_at,omitempty"`
}

type BookPublicationAppealListResult struct {
	Items  []BookPublicationAppealDTO `json:"items"`
	Total  int64                      `json:"total"`
	Limit  int                        `json:"limit"`
	Offset int                        `json:"offset"`
}

func (s *Service) SubmitPublicationAppeal(user authctx.CurrentUser, requestID uuid.UUID, reason string) (BookPublicationAppealDTO, error) {
	if user.ID == uuid.Nil {
		return BookPublicationAppealDTO{}, apperr.Unauthorized("Login required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > bookPublicationAppealReasonMaxLength {
		return BookPublicationAppealDTO{}, apperr.BadRequest("validation.invalid_request", "appeal reason is invalid")
	}
	var appeal model.BookPublicationAppeal
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var request model.BookPublicationRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND submitted_by = ?", requestID, user.ID).First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("books.publication_request_not_found", "Publication request not found")
			}
			return err
		}
		var asset model.PublishedBookAsset
		if err := tx.Where("publication_request_id = ? AND status = ?", request.ID, model.BookPublicationStatusRemoved).First(&asset).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.Conflict("books.appeal_not_available", "Only removed public book assets can be appealed")
			}
			return err
		}
		var pending model.BookPublicationAppeal
		if err := tx.Where("publication_request_id = ? AND status = ?", request.ID, model.BookPublicationAppealStatusPending).First(&pending).Error; err == nil {
			return apperr.Conflict("books.appeal_already_pending", "A publication appeal is already pending")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		appeal = model.BookPublicationAppeal{
			PublicationRequestID: request.ID,
			PublishedAssetID:     asset.ID,
			SubmittedBy:          user.ID,
			Reason:               reason,
			Status:               model.BookPublicationAppealStatusPending,
		}
		if err := tx.Create(&appeal).Error; err != nil {
			return err
		}
		return audit.Record(tx, audit.Entry{
			ActorID: &user.ID, Action: "books.publication_appeal.submit",
			EntityType: "book_publication_appeal", EntityID: &appeal.ID, Reason: reason,
		})
	})
	if err != nil {
		return BookPublicationAppealDTO{}, err
	}
	return buildBookPublicationAppealDTO(appeal), nil
}

func (s *Service) ListMyPublicationAppeals(ctx context.Context, user authctx.CurrentUser, requestID uuid.UUID, limit, offset int) ([]BookPublicationAppealDTO, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Model(&model.BookPublicationAppeal{}).Where("submitted_by = ?", user.ID)
	if requestID != uuid.Nil {
		query = query.Where("publication_request_id = ?", requestID)
	}
	return listBookPublicationAppeals(query, limit, offset)
}

func (s *Service) ListPublicationAppealReviewQueue(ctx context.Context, reviewer authctx.CurrentUser, limit, offset int) ([]BookPublicationAppealDTO, int64, error) {
	if reviewer.ID == uuid.Nil || !authctx.RoleAtLeast(reviewer.Role, authctx.RoleModerator) {
		return nil, 0, apperr.Forbidden("books.review_forbidden", "Book review permission is required")
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Model(&model.BookPublicationAppeal{}).
		Where("status = ? AND submitted_by <> ?", model.BookPublicationAppealStatusPending, reviewer.ID)
	return listBookPublicationAppeals(query, limit, offset)
}

func listBookPublicationAppeals(query *gorm.DB, limit, offset int) ([]BookPublicationAppealDTO, int64, error) {
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var appeals []model.BookPublicationAppeal
	if err := query.Order("created_at ASC").Offset(offset).Limit(limit).Find(&appeals).Error; err != nil {
		return nil, 0, err
	}
	items := make([]BookPublicationAppealDTO, 0, len(appeals))
	for _, appeal := range appeals {
		items = append(items, buildBookPublicationAppealDTO(appeal))
	}
	return items, total, nil
}

func (s *Service) ReviewPublicationAppeal(reviewer authctx.CurrentUser, appealID uuid.UUID, decision, note string) (BookPublicationAppealDTO, error) {
	if reviewer.ID == uuid.Nil || !authctx.RoleAtLeast(reviewer.Role, authctx.RoleModerator) {
		return BookPublicationAppealDTO{}, apperr.Forbidden("books.review_forbidden", "Book review permission is required")
	}
	if decision != model.BookPublicationAppealStatusApproved && decision != model.BookPublicationAppealStatusRejected {
		return BookPublicationAppealDTO{}, apperr.BadRequest("validation.invalid_request", "appeal decision is invalid")
	}
	var appeal model.BookPublicationAppeal
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&appeal, "id = ?", appealID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("books.appeal_not_found", "Publication appeal not found")
			}
			return err
		}
		if appeal.SubmittedBy == reviewer.ID {
			return apperr.Forbidden("books.self_review_forbidden", "You cannot review your own publication appeal")
		}
		if appeal.Status != model.BookPublicationAppealStatusPending {
			return apperr.Conflict("books.appeal_already_reviewed", "Publication appeal is no longer pending")
		}
		if decision == model.BookPublicationAppealStatusApproved {
			result := tx.Model(&model.PublishedBookAsset{}).Where("id = ? AND publication_request_id = ? AND status = ?", appeal.PublishedAssetID, appeal.PublicationRequestID, model.BookPublicationStatusRemoved).Updates(map[string]any{
				"status":         model.BookPublicationStatusPublished,
				"removed_at":     nil,
				"removal_reason": "",
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return apperr.Conflict("books.appeal_asset_changed", "The public book asset is no longer available for restoration")
			}
			if err := tx.Model(&model.BookPublicationRequest{}).Where("id = ?", appeal.PublicationRequestID).Update("status", model.BookPublicationStatusPublished).Error; err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		if err := tx.Model(&appeal).Updates(map[string]any{
			"status":        decision,
			"reviewer_id":   reviewer.ID,
			"reviewed_at":   now,
			"decision_note": strings.TrimSpace(note),
		}).Error; err != nil {
			return err
		}
		action := "books.publication_appeal.reject"
		if decision == model.BookPublicationAppealStatusApproved {
			action = "books.publication_appeal.approve"
		}
		return audit.Record(tx, audit.Entry{
			ActorID: &reviewer.ID, Action: action,
			EntityType: "book_publication_appeal", EntityID: &appeal.ID, Reason: strings.TrimSpace(note),
		})
	})
	if err != nil {
		return BookPublicationAppealDTO{}, err
	}
	return buildBookPublicationAppealDTO(appeal), nil
}

func buildBookPublicationAppealDTO(appeal model.BookPublicationAppeal) BookPublicationAppealDTO {
	return BookPublicationAppealDTO{
		ID: appeal.ID.String(), PublicationRequestID: appeal.PublicationRequestID.String(),
		PublishedAssetID: appeal.PublishedAssetID.String(), Reason: appeal.Reason,
		Status: appeal.Status, DecisionNote: appeal.DecisionNote,
		CreatedAt: appeal.CreatedAt, ReviewedAt: appeal.ReviewedAt,
	}
}
