package books

import (
	"errors"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) RetryBookImport(user authctx.CurrentUser, importID uuid.UUID) (BookImportSessionDTO, error) {
	if user.ID == uuid.Nil {
		return BookImportSessionDTO{}, apperr.Unauthorized("Login required")
	}
	var session model.UserBookImport
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", importID, user.ID).First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bookImportNotFound(err)
			}
			return err
		}
		if session.Status != model.BookImportStatusFailed {
			return apperr.Conflict("books.retry_invalid_status", "Only failed book imports can be retried")
		}
		var asset model.UserBookAsset
		if err := tx.Where("import_id = ? AND user_id = ?", session.ID, user.ID).First(&asset).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("books.asset_not_found", "Book asset not found")
			}
			return err
		}
		if asset.ProcessingStatus == model.BookAssetStatusQuarantined || asset.ScanStatus == "infected" {
			return apperr.Conflict("books.retry_quarantined", "Quarantined book assets cannot be retried")
		}
		if err := tx.Model(&model.UserBookAsset{}).Where("id = ? AND processing_status = ?", asset.ID, model.BookAssetStatusFailed).Updates(map[string]any{
			"processing_status": model.BookAssetStatusScanning,
			"scan_status":       "pending",
			"error_message":     "",
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.UserBookImport{}).Where("id = ? AND status = ?", session.ID, model.BookImportStatusFailed).Updates(map[string]any{
			"status": model.BookImportStatusScanning, "error_code": "", "error_message": "",
		}).Error
	})
	if err != nil {
		return BookImportSessionDTO{}, err
	}
	session.Status = model.BookImportStatusScanning
	asset, err := s.findBookAsset(session.ID)
	if err != nil {
		return BookImportSessionDTO{}, err
	}
	if asset != nil {
		asset.ProcessingStatus = model.BookAssetStatusScanning
		asset.ScanStatus = "pending"
		asset.ErrorMessage = ""
	}
	return buildBookImportSessionDTO(session, asset), nil
}
