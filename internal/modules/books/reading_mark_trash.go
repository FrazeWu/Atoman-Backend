package books

import (
	"context"
	"errors"
	"net/http"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Deleted highlight content is recoverable for 30 days; ID tombstones stay
// forever to reject offline edits that would resurrect an erased highlight.
const bookReadingMarkTrashRetention = 30 * 24 * time.Hour

type BookReadingMarkTrashItem struct {
	BookReadingMarkDTO
	WorkID       uuid.UUID `json:"work_id"`
	AssetID      uuid.UUID `json:"asset_id"`
	WorkTitle    string    `json:"work_title"`
	RestoreUntil time.Time `json:"restore_until"`
}
type BookReadingMarkTrashResult struct {
	Items []BookReadingMarkTrashItem `json:"items"`
	Total int64                      `json:"total"`
}

func (s *Service) purgeBookReadingMarks(ctx context.Context, userID *uuid.UUID, now time.Time) error {
	q := s.db.WithContext(ctx).Unscoped().Model(&model.BookReadingMark{}).Where("deleted_at IS NOT NULL AND deleted_at <= ? AND purged_at IS NULL", now.Add(-bookReadingMarkTrashRetention))
	if userID != nil {
		q = q.Where("user_id = ?", *userID)
	}
	return q.Updates(map[string]any{"label": "", "quote": "", "note": "", "anchor_json": "{}", "color": "", "style": "", "purged_at": now, "version": gorm.Expr("version + 1")}).Error
}
func (s *Service) ListBookReadingMarkTrash(ctx context.Context, user authctx.CurrentUser, limit, offset int) (BookReadingMarkTrashResult, error) {
	result := BookReadingMarkTrashResult{Items: []BookReadingMarkTrashItem{}}
	if user.ID == uuid.Nil {
		return result, apperr.Unauthorized("Login required")
	}
	if err := s.purgeBookReadingMarks(ctx, &user.ID, time.Now().UTC()); err != nil {
		return result, err
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	db := s.db.WithContext(ctx).Model(&model.BookReadingMark{}).Unscoped().
		Joins("JOIN book_asset_revisions r ON r.id = book_reading_marks.revision_id").
		Joins("JOIN book_assets a ON a.id = r.asset_id").
		Joins("JOIN book_works w ON w.id = a.work_id").
		Where("book_reading_marks.user_id = ? AND book_reading_marks.kind = 'highlight' AND book_reading_marks.deleted_at IS NOT NULL AND book_reading_marks.deleted_at > ? AND book_reading_marks.purged_at IS NULL", user.ID, time.Now().UTC().Add(-bookReadingMarkTrashRetention))
	if err := db.Count(&result.Total).Error; err != nil {
		return result, err
	}
	var rows []struct {
		model.BookReadingMark
		WorkID    uuid.UUID
		AssetID   uuid.UUID
		WorkTitle string
	}
	if err := db.Select("book_reading_marks.*, a.work_id, a.id AS asset_id, w.title AS work_title").Order("book_reading_marks.deleted_at DESC, book_reading_marks.id ASC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return result, err
	}
	for _, row := range rows {
		mark, err := bookReadingMarkDTO(row.BookReadingMark)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, BookReadingMarkTrashItem{BookReadingMarkDTO: mark, WorkID: row.WorkID, AssetID: row.AssetID, WorkTitle: row.WorkTitle, RestoreUntil: row.DeletedAt.Time.Add(bookReadingMarkTrashRetention).UTC()})
	}
	return result, nil
}

func (s *Service) RestoreBookReadingMark(ctx context.Context, user authctx.CurrentUser, revisionID, markID uuid.UUID, baseVersion int64) (BookReadingMarkDTO, error) {
	if user.ID == uuid.Nil {
		return BookReadingMarkDTO{}, apperr.Unauthorized("Login required")
	}
	if revisionID == uuid.Nil || markID == uuid.Nil || baseVersion < 1 {
		return BookReadingMarkDTO{}, apperr.BadRequest("books.invalid_reading_mark", "Revision, mark UUID and current version required")
	}
	if err := s.purgeBookReadingMarks(ctx, &user.ID, time.Now().UTC()); err != nil {
		return BookReadingMarkDTO{}, err
	}
	var result BookReadingMarkDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var mark model.BookReadingMark
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND revision_id = ? AND kind = 'highlight'", markID, user.ID, revisionID).First(&mark).Error; err != nil {
			return privateBookReadingMarkError(err)
		}
		if mark.PurgedAt != nil || (mark.DeletedAt.Valid && !mark.DeletedAt.Time.After(time.Now().UTC().Add(-bookReadingMarkTrashRetention))) {
			return apperr.New(http.StatusGone, "books.reading_mark_expired", "The 30-day restore period has expired", nil)
		}
		if !mark.DeletedAt.Valid {
			if mark.Version != baseVersion+1 {
				return bookReadingMarkConflict(mark)
			}
		} else {
			if mark.Version != baseVersion {
				return bookReadingMarkConflict(mark)
			}
			mark.Version++
			mark.DeletedAt = gorm.DeletedAt{}
			if err := tx.Unscoped().Model(&mark).Select("version", "deleted_at", "updated_at").Updates(&mark).Error; err != nil {
				return err
			}
		}
		var err error
		result, err = bookReadingMarkDTO(mark)
		return err
	})
	return result, err
}

func (s *Service) PermanentlyEraseBookReadingMark(ctx context.Context, user authctx.CurrentUser, revisionID, markID uuid.UUID, baseVersion int64) (BookReadingMarkTombstone, error) {
	if user.ID == uuid.Nil {
		return BookReadingMarkTombstone{}, apperr.Unauthorized("Login required")
	}
	if revisionID == uuid.Nil || markID == uuid.Nil || baseVersion < 1 {
		return BookReadingMarkTombstone{}, apperr.BadRequest("books.invalid_reading_mark", "Revision, mark UUID and current version required")
	}
	var result BookReadingMarkTombstone
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var mark model.BookReadingMark
		err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND revision_id = ? AND kind = 'highlight'", markID, user.ID, revisionID).First(&mark).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return privateBookReadingMarkError(err)
		}
		if err != nil {
			return err
		}
		if !mark.DeletedAt.Valid {
			return apperr.BadRequest("books.reading_mark_not_trashed", "Only trashed marks can be erased")
		}
		if mark.PurgedAt != nil {
			if mark.Version != baseVersion+1 {
				return bookReadingMarkConflict(mark)
			}
		} else {
			if mark.Version != baseVersion {
				return bookReadingMarkConflict(mark)
			}
			now := time.Now().UTC()
			mark.Version++
			mark.PurgedAt = &now
			if err := tx.Unscoped().Model(&mark).Updates(map[string]any{"label": "", "quote": "", "note": "", "anchor_json": "{}", "color": "", "style": "", "purged_at": now, "version": mark.Version}).Error; err != nil {
				return err
			}
		}
		result = BookReadingMarkTombstone{ID: mark.ID, Version: mark.Version, DeletedAt: mark.DeletedAt.Time.UTC()}
		return nil
	})
	return result, err
}
