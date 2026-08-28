package books

import (
	"context"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const bookPublicationReportMaxLength = 5000

type BookPublicationReportDTO struct {
	ID           string     `json:"id"`
	AssetID      string     `json:"asset_id"`
	Reason       string     `json:"reason"`
	Status       string     `json:"status"`
	DecisionNote string     `json:"decision_note,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
}

type BookPublicationReportListResult struct {
	Items  []BookPublicationReportDTO `json:"items"`
	Total  int64                      `json:"total"`
	Limit  int                        `json:"limit"`
	Offset int                        `json:"offset"`
}

func (s *Service) ReportPublishedBookAsset(user authctx.CurrentUser, assetID uuid.UUID, reason string) (BookPublicationReportDTO, error) {
	if user.ID == uuid.Nil {
		return BookPublicationReportDTO{}, apperr.Unauthorized("Login required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > bookPublicationReportMaxLength {
		return BookPublicationReportDTO{}, apperr.BadRequest("validation.invalid_request", "report reason is invalid")
	}
	var asset model.PublishedBookAsset
	if err := s.db.Where("id = ? AND status = ?", assetID, model.BookPublicationStatusPublished).First(&asset).Error; err != nil {
		return BookPublicationReportDTO{}, apperr.NotFound("books.published_asset_not_found", "Published book asset not found")
	}
	var existing model.BookPublicationReport
	if err := s.db.Where("asset_id = ? AND reporter_id = ?", assetID, user.ID).First(&existing).Error; err == nil {
		if existing.Status == model.BookPublicationReportStatusPending {
			return BookPublicationReportDTO{}, apperr.Conflict("books.report_already_exists", "A report is already pending")
		}
		if existing.Status == model.BookPublicationReportStatusRejected {
			if err := s.db.Model(&existing).Updates(map[string]any{"reason": reason, "status": model.BookPublicationReportStatusPending, "reviewer_id": nil, "reviewed_at": nil, "decision_note": ""}).Error; err != nil {
				return BookPublicationReportDTO{}, err
			}
			existing.Reason, existing.Status, existing.ReviewerID, existing.ReviewedAt, existing.DecisionNote = reason, model.BookPublicationReportStatusPending, nil, nil, ""
			return buildBookPublicationReportDTO(existing), nil
		}
		return BookPublicationReportDTO{}, apperr.Conflict("books.report_already_resolved", "This publication report has already been resolved")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return BookPublicationReportDTO{}, err
	}
	report := model.BookPublicationReport{AssetID: assetID, ReporterID: user.ID, Reason: reason, Status: model.BookPublicationReportStatusPending}
	if err := s.db.Create(&report).Error; err != nil {
		return BookPublicationReportDTO{}, err
	}
	return buildBookPublicationReportDTO(report), nil
}

func (s *Service) ListPublicationReports(ctx context.Context, reviewer authctx.CurrentUser, limit, offset int) ([]BookPublicationReportDTO, int64, error) {
	if reviewer.ID == uuid.Nil || !authctx.RoleAtLeast(reviewer.Role, authctx.RoleModerator) {
		return nil, 0, apperr.Forbidden("books.review_forbidden", "Book review permission is required")
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Model(&model.BookPublicationReport{}).Where("status = ? AND reporter_id <> ?", model.BookPublicationReportStatusPending, reviewer.ID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var reports []model.BookPublicationReport
	if err := query.Order("created_at ASC").Offset(offset).Limit(limit).Find(&reports).Error; err != nil {
		return nil, 0, err
	}
	items := make([]BookPublicationReportDTO, 0, len(reports))
	for _, report := range reports {
		items = append(items, buildBookPublicationReportDTO(report))
	}
	return items, total, nil
}

func (s *Service) ReviewPublicationReport(reviewer authctx.CurrentUser, reportID uuid.UUID, decision, note string) (BookPublicationReportDTO, error) {
	if reviewer.ID == uuid.Nil || !authctx.RoleAtLeast(reviewer.Role, authctx.RoleModerator) {
		return BookPublicationReportDTO{}, apperr.Forbidden("books.review_forbidden", "Book review permission is required")
	}
	if decision != model.BookPublicationReportStatusRemoved && decision != model.BookPublicationReportStatusRejected {
		return BookPublicationReportDTO{}, apperr.BadRequest("validation.invalid_request", "report decision is invalid")
	}
	var report model.BookPublicationReport
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", reportID).First(&report).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("books.report_not_found", "Publication report not found")
			}
			return err
		}
		if report.ReporterID == reviewer.ID {
			return apperr.Forbidden("books.self_review_forbidden", "You cannot review your own report")
		}
		if report.Status != model.BookPublicationReportStatusPending {
			return apperr.Conflict("books.report_already_reviewed", "Publication report is no longer pending")
		}
		now := time.Now().UTC()
		if err := tx.Model(&report).Updates(map[string]any{"status": decision, "reviewer_id": reviewer.ID, "reviewed_at": now, "decision_note": strings.TrimSpace(note)}).Error; err != nil {
			return err
		}
		if decision == model.BookPublicationReportStatusRemoved {
			return tx.Model(&model.PublishedBookAsset{}).Where("id = ? AND status = ?", report.AssetID, model.BookPublicationStatusPublished).Updates(map[string]any{"status": model.BookPublicationStatusRemoved, "removed_at": now, "removal_reason": "removed after publication report"}).Error
		}
		return nil
	})
	if err != nil {
		return BookPublicationReportDTO{}, err
	}
	return buildBookPublicationReportDTO(report), nil
}

func buildBookPublicationReportDTO(report model.BookPublicationReport) BookPublicationReportDTO {
	return BookPublicationReportDTO{ID: report.ID.String(), AssetID: report.AssetID.String(), Reason: report.Reason, Status: report.Status, DecisionNote: report.DecisionNote, CreatedAt: report.CreatedAt, ReviewedAt: report.ReviewedAt}
}
