package books

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type EditionFileDTO struct {
	AssetID    uuid.UUID              `json:"asset_id"`
	RevisionID uuid.UUID              `json:"revision_id"`
	EditionID  uuid.UUID              `json:"edition_id"`
	WorkID     uuid.UUID              `json:"work_id"`
	Title      string                 `json:"title"`
	Format     string                 `json:"format"`
	Language   string                 `json:"language"`
	FileName   string                 `json:"file_name"`
	IsPublic   bool                   `json:"is_public"`
	CanManage  bool                   `json:"can_manage"`
	CreatedBy  uuid.UUID              `json:"-"`
	IsBest     bool                   `json:"is_best"`
	Quality    *BookFileQualityReport `json:"quality,omitempty" gorm:"-"`
	CreatedAt  time.Time              `json:"created_at"`
}

func (s *Service) ListBookEditionFiles(ctx context.Context, editionID uuid.UUID) ([]EditionFileDTO, error) {
	if editionID == uuid.Nil {
		return nil, apperr.BadRequest("books.invalid_edition", "Edition ID is required")
	}
	var edition model.BookEdition
	if err := s.db.WithContext(ctx).Where("id = ? AND lifecycle_status = ?", editionID, model.BookLifecycleStatusActive).First(&edition).Error; err != nil {
		return nil, publicBookNotFound(err)
	}
	var files []EditionFileDTO
	err := s.db.WithContext(ctx).Table("book_asset_revisions AS rev").
		Select("asset.id AS asset_id, rev.id AS revision_id, asset.edition_id, asset.work_id, asset.title, asset.format, asset.language, rev.original_filename AS file_name, rev.created_at, rev.created_by, COALESCE(vis.public,FALSE) AS is_public, COALESCE(best.revision_id = rev.id, FALSE) AS is_best").
		Joins("JOIN book_assets AS asset ON asset.id = rev.asset_id AND asset.deleted_at IS NULL").
		Joins("LEFT JOIN book_edition_best_files AS best ON best.edition_id = asset.edition_id").
		Joins("LEFT JOIN book_file_visibilities AS vis ON vis.revision_id = rev.id").
		Where("asset.edition_id = ? AND asset.status = ? AND rev.status = ? AND rev.deleted_at IS NULL", editionID, model.BookResourceStatusActive, model.BookRevisionStatusReady).
		Order("is_best DESC, rev.created_at DESC, rev.id DESC").Scan(&files).Error
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = []EditionFileDTO{}
	}
	if len(files) != 0 {
		revisionIDs := make([]uuid.UUID, 0, len(files))
		for _, item := range files {
			revisionIDs = append(revisionIDs, item.RevisionID)
		}
		var qualities []model.BookFileQuality
		if err := s.db.WithContext(ctx).Where("revision_id IN ?", revisionIDs).Find(&qualities).Error; err != nil {
			return nil, err
		}
		byRevision := make(map[uuid.UUID]BookFileQualityReport, len(qualities))
		for _, row := range qualities {
			var report BookFileQualityReport
			if json.Unmarshal([]byte(row.ReportJSON), &report) == nil && report.Version == 1 {
				byRevision[row.RevisionID] = report
			}
		}
		for i := range files {
			if report, ok := byRevision[files[i].RevisionID]; ok {
				copyReport := report
				files[i].Quality = &copyReport
			}
		}
	}
	return files, nil
}

// Best-file selection changes only the edition recommendation, not readers'
// personal file selections, shelf entries, or annotations.
func (s *Service) ListBookEditionFilesForViewer(ctx context.Context, editionID, userID uuid.UUID) ([]EditionFileDTO, error) {
	files, err := s.ListBookEditionFiles(ctx, editionID)
	if err != nil {
		return nil, err
	}
	visible := make([]EditionFileDTO, 0, len(files))
	for _, file := range files {
		file.CanManage = userID != uuid.Nil && file.CreatedBy == userID
		if file.IsPublic || file.CanManage {
			visible = append(visible, file)
		}
	}
	return visible, nil
}

func (s *Service) SetBookEditionBestFile(ctx context.Context, user authctx.CurrentUser, editionID, revisionID uuid.UUID) (model.BookEditionBestFile, error) {
	var result model.BookEditionBestFile
	if user.ID == uuid.Nil {
		return result, apperr.Unauthorized("Login required")
	}
	if editionID == uuid.Nil || revisionID == uuid.Nil {
		return result, apperr.BadRequest("books.invalid_best_file", "Edition and file revision are required")
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var edition model.BookEdition
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND lifecycle_status = ? AND edit_status <> ?", editionID, model.BookLifecycleStatusActive, model.BookEditStatusClosed).First(&edition).Error; err != nil {
			return publicBookNotFound(err)
		}
		var revision model.BookAssetRevision
		if err := tx.Joins("JOIN book_assets AS a ON a.id = book_asset_revisions.asset_id").
			Where("book_asset_revisions.id = ? AND book_asset_revisions.status = ? AND a.edition_id = ? AND a.work_id = ? AND a.status = ?", revisionID, model.BookRevisionStatusReady, editionID, edition.WorkID, model.BookResourceStatusActive).First(&revision).Error; err != nil {
			return apperr.BadRequest("books.invalid_best_file", "File is unavailable or belongs to a different edition")
		}
		if revision.CreatedBy != user.ID {
			var vis model.BookFileVisibility
			if err := tx.Where("revision_id = ? AND public = TRUE", revisionID).First(&vis).Error; err != nil {
				return apperr.Forbidden("books.file_private", "Cannot recommend another user's private file")
			}
		}
		var old model.BookEditionBestFile
		loadErr := tx.Where("edition_id = ?", editionID).First(&old).Error
		if loadErr != nil && !errors.Is(loadErr, gorm.ErrRecordNotFound) {
			return loadErr
		}
		if loadErr == nil && old.RevisionID == revisionID {
			result = old
			return nil
		}
		result = model.BookEditionBestFile{EditionID: editionID, AssetID: revision.AssetID, RevisionID: revisionID, UpdatedBy: user.ID, UpdatedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "edition_id"}}, DoUpdates: clause.AssignmentColumns([]string{"asset_id", "revision_id", "updated_by", "updated_at"})}).Create(&result).Error; err != nil {
			return err
		}
		event := model.BookBestFileEvent{EditionID: editionID, ToRevisionID: revisionID, ChangedBy: user.ID}
		if old.RevisionID != uuid.Nil {
			event.FromRevisionID = &old.RevisionID
		}
		return tx.Create(&event).Error
	})
	return result, err
}
