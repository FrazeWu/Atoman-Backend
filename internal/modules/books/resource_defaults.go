package books

import (
	"context"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SetBookDefaultAssetInput struct {
	AssetID uuid.UUID `json:"asset_id"`
}

func (s *Service) SetBookDefaultAsset(ctx context.Context, user authctx.CurrentUser, workID uuid.UUID, rawLanguage string, assetID uuid.UUID) (model.BookDefaultAsset, error) {
	if user.ID == uuid.Nil {
		return model.BookDefaultAsset{}, apperr.Unauthorized("Login required")
	}
	lang, err := normalizeBookResourceLanguage(rawLanguage)
	if err != nil {
		return model.BookDefaultAsset{}, err
	}
	value := model.BookDefaultAsset{WorkID: workID, Language: lang, AssetID: assetID, UpdatedBy: user.ID, UpdatedAt: time.Now().UTC()}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var work model.BookWork
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND lifecycle_status = ? AND edit_status <> ?", workID, model.BookLifecycleStatusActive, model.BookEditStatusClosed).First(&work).Error; err != nil {
			return publicBookNotFound(err)
		}
		var asset model.BookAsset
		if err := tx.Clauses(clause.Locking{Strength: "SHARE", Table: clause.Table{Name: "book_assets"}}).
			Joins("JOIN book_asset_revisions r ON r.id = book_assets.current_revision_id AND r.deleted_at IS NULL AND r.status = ?", model.BookRevisionStatusReady).
			Where("book_assets.id = ? AND book_assets.work_id = ? AND book_assets.language = ? AND book_assets.status = ?", assetID, workID, lang, model.BookResourceStatusActive).First(&asset).Error; err != nil {
			return publicBookNotFound(err)
		}
		// This updates only the community default, never personal selections.
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "work_id"}, {Name: "language"}}, DoUpdates: clause.AssignmentColumns([]string{"asset_id", "updated_by", "updated_at"})}).Create(&value).Error
	})
	return value, err
}
