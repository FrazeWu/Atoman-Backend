package books

import (
	"context"
	"errors"
	"fmt"
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

const (
	bookRetentionBatchSize       = 32
	bookEvidenceRetentionYears   = 2
	bookAuditRetentionYears      = 7
	bookRetentionReasonMaxLength = 5000
)

// BookPublicationRetentionHoldDTO is the internal moderation result for a request hold.
type BookPublicationRetentionHoldDTO struct {
	RequestID string     `json:"request_id"`
	Held      bool       `json:"held"`
	Reason    string     `json:"reason,omitempty"`
	SetAt     *time.Time `json:"set_at,omitempty"`
}

// ProcessBookRetention removes expired authorization evidence and resolved appeal
// materials. Audit logs remain append-only and are retained for at least seven years.
func (s *Service) ProcessBookRetention(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("book retention database is required")
	}
	if err := requireBookUploadStore(s.bookUpload); err != nil {
		return 0, err
	}
	var candidates []struct {
		ID         uuid.UUID  `gorm:"column:id"`
		ReviewedAt *time.Time `gorm:"column:reviewed_at"`
	}
	if err := s.db.WithContext(ctx).Model(&model.BookPublicationRequest{}).
		Select("book_publication_requests.id, book_publication_requests.reviewed_at").
		Joins("LEFT JOIN book_rights_declarations ON book_rights_declarations.request_id = book_publication_requests.id").
		Joins("LEFT JOIN book_publication_appeals ON book_publication_appeals.publication_request_id = book_publication_requests.id").
		Where("book_publication_requests.status <> ? AND book_publication_requests.reviewed_at IS NOT NULL", model.BookPublicationStatusPendingReview).
		Where("book_rights_declarations.evidence_object_key <> '' OR book_publication_appeals.id IS NOT NULL").
		Distinct().Order("book_publication_requests.reviewed_at ASC").Limit(bookRetentionBatchSize).Scan(&candidates).Error; err != nil {
		return 0, err
	}

	now := time.Now().UTC()
	cleaned := 0
	var retentionErr error
	for _, candidate := range candidates {
		changed, err := s.cleanupPublicationRetention(ctx, candidate.ID, now)
		if err != nil {
			retentionErr = errors.Join(retentionErr, fmt.Errorf("cleanup book publication request %s: %w", candidate.ID, err))
			continue
		}
		if changed {
			cleaned++
		}
	}
	return cleaned, retentionErr
}

func (s *Service) cleanupPublicationRetention(ctx context.Context, requestID uuid.UUID, now time.Time) (bool, error) {
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var request model.BookPublicationRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", requestID).First(&request).Error; err != nil {
			return err
		}
		if request.Status == model.BookPublicationStatusPendingReview || request.RetentionHold || request.ReviewedAt == nil {
			return nil
		}

		var rights model.BookRightsDeclaration
		rightsErr := tx.Where("request_id = ?", request.ID).First(&rights).Error
		if rightsErr != nil && !errors.Is(rightsErr, gorm.ErrRecordNotFound) {
			return rightsErr
		}
		var appeals []model.BookPublicationAppeal
		if err := tx.Where("publication_request_id = ?", request.ID).Find(&appeals).Error; err != nil {
			return err
		}
		finalAt := *request.ReviewedAt
		for _, appeal := range appeals {
			if appeal.Status == model.BookPublicationAppealStatusPending {
				return nil
			}
			if appeal.ReviewedAt != nil && appeal.ReviewedAt.After(finalAt) {
				finalAt = *appeal.ReviewedAt
			}
		}
		if finalAt.AddDate(bookEvidenceRetentionYears, 0, 0).After(now) {
			return nil
		}

		evidenceDeleted := false
		if rightsErr == nil && strings.TrimSpace(rights.EvidenceObjectKey) != "" {
			if err := s.bookUpload.DeleteObject(rights.EvidenceObjectKey); err != nil {
				return err
			}
			deletedAt := now
			result := tx.Model(&model.BookRightsDeclaration{}).
				Where("id = ? AND evidence_object_key = ?", rights.ID, rights.EvidenceObjectKey).
				Updates(map[string]any{
					"evidence_object_key": "", "evidence_file_name": "", "evidence_content_type": "",
					"evidence_size_bytes": 0, "evidence_deleted_at": &deletedAt,
				})
			if result.Error != nil {
				return result.Error
			}
			evidenceDeleted = result.RowsAffected == 1
		}

		appealResult := tx.Where("publication_request_id = ? AND status <> ? AND reviewed_at IS NOT NULL", request.ID, model.BookPublicationAppealStatusPending).Delete(&model.BookPublicationAppeal{})
		if appealResult.Error != nil {
			return appealResult.Error
		}
		if evidenceDeleted || appealResult.RowsAffected > 0 {
			if err := audit.Record(tx, audit.Entry{
				Action:     "books.retention.cleanup",
				EntityType: "book_publication_request",
				EntityID:   &request.ID,
				Reason:     "authorization evidence and resolved appeal materials expired after retention period",
				Metadata: map[string]any{
					"evidence_deleted": evidenceDeleted,
					"appeals_deleted":  appealResult.RowsAffected,
					"evidence_years":   bookEvidenceRetentionYears,
					"audit_years":      bookAuditRetentionYears,
				},
			}); err != nil {
				return err
			}
			changed = true
		}
		return nil
	})
	return changed, err
}

func (s *Service) SetPublicationRetentionHold(reviewer authctx.CurrentUser, requestID uuid.UUID, held bool, reason string) (BookPublicationRetentionHoldDTO, error) {
	if reviewer.ID == uuid.Nil || !authctx.RoleAtLeast(reviewer.Role, authctx.RoleModerator) {
		return BookPublicationRetentionHoldDTO{}, apperr.Forbidden("books.review_forbidden", "Book review permission is required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > bookRetentionReasonMaxLength {
		return BookPublicationRetentionHoldDTO{}, apperr.BadRequest("validation.invalid_request", "retention hold reason is invalid")
	}
	var request model.BookPublicationRequest
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", requestID).First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("books.publication_request_not_found", "Publication request not found")
			}
			return err
		}
		now := time.Now().UTC()
		action := "books.retention_hold.release"
		var updateErr error
		if held {
			updateErr = tx.Model(&request).Updates(map[string]any{
				"retention_hold":        true,
				"retention_hold_reason": reason,
				"retention_hold_set_by": reviewer.ID,
				"retention_hold_set_at": now,
			}).Error
			action = "books.retention_hold.set"
		} else {
			updateErr = tx.Exec(`UPDATE book_publication_requests
SET retention_hold = FALSE, retention_hold_reason = '', retention_hold_set_by = NULL, retention_hold_set_at = NULL, updated_at = NOW()
WHERE id = ?`, request.ID).Error
		}
		if updateErr != nil {
			return updateErr
		}
		if err := audit.Record(tx, audit.Entry{
			ActorID: &reviewer.ID, Action: action, EntityType: "book_publication_request", EntityID: &request.ID, Reason: reason,
			Metadata: map[string]any{"held": held},
		}); err != nil {
			return err
		}
		request.RetentionHold = held
		if held {
			request.RetentionHoldReason = reason
			request.RetentionHoldSetBy = &reviewer.ID
			request.RetentionHoldSetAt = &now
		} else {
			request.RetentionHoldReason = ""
			request.RetentionHoldSetBy = nil
			request.RetentionHoldSetAt = nil
		}
		return nil
	})
	if err != nil {
		return BookPublicationRetentionHoldDTO{}, err
	}
	return BookPublicationRetentionHoldDTO{RequestID: request.ID.String(), Held: request.RetentionHold, Reason: request.RetentionHoldReason, SetAt: request.RetentionHoldSetAt}, nil
}
