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

type SaveBookFileShelfInput struct {
	Status   string `json:"status"`
	Archived *bool  `json:"archived,omitempty"`
}

type BookFileShelfDTO struct {
	ID              uuid.UUID  `json:"id"`
	RevisionID      uuid.UUID  `json:"revision_id"`
	AssetID         uuid.UUID  `json:"asset_id"`
	WorkID          uuid.UUID  `json:"work_id"`
	EditionID       *uuid.UUID `json:"edition_id,omitempty"`
	Title           string     `json:"title"`
	CoverURL        string     `json:"cover_url"`
	Format          string     `json:"format"`
	Status          string     `json:"status"`
	Archived        bool       `json:"archived"`
	FurthestPercent float64    `json:"furthest_percent"`
	HasBetter       bool       `json:"has_better"`
	FileAvailable   bool       `json:"file_available"`
	CreatedAt       time.Time  `json:"created_at"`
	LastReadAt      *time.Time `json:"last_read_at,omitempty"`
}

func validateBookFileShelfStatus(status string) error {
	switch status {
	case model.BookFileShelfUnstarted, model.BookFileShelfReading, model.BookFileShelfFinished, model.BookFileShelfPaused:
		return nil
	default:
		return apperr.BadRequest("books.invalid_shelf_status", "Unknown reading status")
	}
}

func (s *Service) SaveBookFileShelf(ctx context.Context, user authctx.CurrentUser, revisionID uuid.UUID, input SaveBookFileShelfInput) (model.BookFileShelf, error) {
	var row model.BookFileShelf
	if user.ID == uuid.Nil {
		return row, apperr.Unauthorized("Login required")
	}
	if revisionID == uuid.Nil {
		return row, apperr.BadRequest("books.invalid_revision", "A specific file is required")
	}
	if input.Status != "" {
		if err := validateBookFileShelfStatus(input.Status); err != nil {
			return row, err
		}
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.BookFileShelf
		findErr := tx.Where("user_id = ? AND revision_id = ?", user.ID, revisionID).First(&existing).Error
		if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			service := *s
			service.db = tx
			if err := service.CanReadBookRevision(ctx, user.ID, revisionID); err != nil {
				return err
			}
			row = model.BookFileShelf{UserID: user.ID, RevisionID: revisionID, Status: model.BookFileShelfUnstarted}
			if input.Status != "" {
				row.Status = input.Status
			}
			if input.Archived != nil {
				row.Archived = *input.Archived
			}
			return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "revision_id"}}, DoNothing: true}).Create(&row).Error
		}
		row = existing
		updates := map[string]any{}
		if input.Status != "" {
			updates["status"] = input.Status
			row.Status = input.Status
		}
		if input.Archived != nil {
			updates["archived"] = *input.Archived
			row.Archived = *input.Archived
		}
		if len(updates) > 0 {
			return tx.Model(&existing).Updates(updates).Error
		}
		return nil
	})
	return row, err
}

func (s *Service) ListBookFileShelf(ctx context.Context, user authctx.CurrentUser, status, sortBy string, limit, offset int) ([]BookFileShelfDTO, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	status = strings.TrimSpace(status)
	if status != "" {
		if err := validateBookFileShelfStatus(status); err != nil {
			return nil, 0, err
		}
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Table("book_file_shelves AS shelf").Where("shelf.user_id = ? AND shelf.deleted_at IS NULL", user.ID)
	if status != "" {
		query = query.Where("shelf.status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	order := "GREATEST(shelf.created_at, COALESCE(reading.updated_at, shelf.created_at)) DESC, shelf.id DESC"
	switch sortBy {
	case "added":
		order = "shelf.created_at DESC, shelf.id DESC"
	case "progress":
		order = "COALESCE(reading.furthest_percent, 0) DESC, shelf.created_at DESC"
	case "title":
		order = "work.title ASC, shelf.created_at DESC"
	case "activity", "":
	default:
		return nil, 0, apperr.BadRequest("books.invalid_shelf_sort", "Unknown shelf sort")
	}
	var rows []BookFileShelfDTO
	err := query.Select("shelf.id, shelf.revision_id, asset.id AS asset_id, asset.work_id, asset.edition_id, work.title AS title, COALESCE(edition.cover_url, '') AS cover_url, asset.format, shelf.status, shelf.archived, shelf.created_at, COALESCE(reading.furthest_percent, 0) AS furthest_percent, reading.updated_at AS last_read_at, " +
		"COALESCE(best.revision_id IS NOT NULL AND best.revision_id <> shelf.revision_id AND (best_rev.created_by = shelf.user_id OR COALESCE(best_vis.public,FALSE)), FALSE) AS has_better, " +
		"(rev.status = 'ready' AND asset.status = 'active' AND work.lifecycle_status = 'active' AND (rev.created_by = shelf.user_id OR COALESCE(vis.public,FALSE))) AS file_available").
		Joins("JOIN book_asset_revisions AS rev ON rev.id = shelf.revision_id").
		Joins("JOIN book_assets AS asset ON asset.id = rev.asset_id").
		Joins("JOIN book_works AS work ON work.id = asset.work_id").
		Joins("LEFT JOIN book_editions AS edition ON edition.id = asset.edition_id").
		Joins("LEFT JOIN book_edition_best_files AS best ON best.edition_id = asset.edition_id").
		Joins("LEFT JOIN book_asset_revisions AS best_rev ON best_rev.id = best.revision_id").
		Joins("LEFT JOIN book_file_visibilities AS best_vis ON best_vis.revision_id = best.revision_id").
		Joins("LEFT JOIN book_file_visibilities AS vis ON vis.revision_id = rev.id").
		Joins("LEFT JOIN book_reading_states AS reading ON reading.user_id = shelf.user_id AND reading.revision_id = shelf.revision_id").
		Order(order).Limit(limit).Offset(offset).Scan(&rows).Error
	if rows == nil {
		rows = []BookFileShelfDTO{}
	}
	return rows, total, err
}

func (s *Service) DeleteBookFileShelf(ctx context.Context, user authctx.CurrentUser, revisionID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if revisionID == uuid.Nil {
		return apperr.BadRequest("books.invalid_revision", "File revision is required")
	}
	return s.db.WithContext(ctx).Unscoped().Where("user_id = ? AND revision_id = ?", user.ID, revisionID).Delete(&model.BookFileShelf{}).Error
}
