package books

import (
	"context"
	"errors"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type LinkBookImportInput struct {
	WorkID    string `json:"work_id"`
	EditionID string `json:"edition_id"`
}

func (s *Service) LinkBookImportToCatalog(user authctx.CurrentUser, importID uuid.UUID, input LinkBookImportInput) (BookImportSessionDTO, error) {
	if user.ID == uuid.Nil {
		return BookImportSessionDTO{}, apperr.Unauthorized("Login required")
	}
	workID, editionID, err := parseImportCatalogTargets(input)
	if err != nil {
		return BookImportSessionDTO{}, err
	}
	if workID != nil {
		if _, err := s.GetPublicWork(context.Background(), *workID); err != nil {
			return BookImportSessionDTO{}, err
		}
	}
	if editionID != nil {
		edition, err := s.GetPublicEdition(context.Background(), *editionID)
		if err != nil {
			return BookImportSessionDTO{}, err
		}
		if workID != nil && edition.Edition.WorkID != workID.String() {
			return BookImportSessionDTO{}, apperr.BadRequest("validation.invalid_request", "edition does not belong to work")
		}
		if workID == nil {
			parsedWorkID, parseErr := uuid.Parse(edition.Work.ID)
			if parseErr != nil {
				return BookImportSessionDTO{}, parseErr
			}
			workID = &parsedWorkID
		}
	}
	var session model.UserBookImport
	var asset *model.UserBookAsset
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", importID, user.ID).First(&session).Error; err != nil {
			return bookImportNotFound(err)
		}
		if session.Status == model.BookImportStatusDeleted {
			return apperr.NotFound("books.import_not_found", "Book import not found")
		}
		session.WorkID, session.EditionID = workID, editionID
		if err := tx.Model(&session).Updates(map[string]any{"work_id": workID, "edition_id": editionID}).Error; err != nil {
			return err
		}
		var source model.UserBookAsset
		if err := tx.Where("import_id = ? AND user_id = ?", session.ID, user.ID).First(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		asset = &source
		return tx.Model(&model.PublishedBookAsset{}).Where("source_asset_id = ?", source.ID).Updates(map[string]any{
			"work_id": workID, "edition_id": editionID,
		}).Error
	})
	if err != nil {
		return BookImportSessionDTO{}, err
	}
	return buildBookImportSessionDTO(s.db, session, asset), nil
}

func parseImportCatalogTargets(input LinkBookImportInput) (*uuid.UUID, *uuid.UUID, error) {
	var workID, editionID *uuid.UUID
	if strings.TrimSpace(input.WorkID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(input.WorkID))
		if err != nil || id == uuid.Nil {
			return nil, nil, apperr.BadRequest("validation.invalid_request", "work_id is invalid")
		}
		workID = &id
	}
	if strings.TrimSpace(input.EditionID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(input.EditionID))
		if err != nil || id == uuid.Nil {
			return nil, nil, apperr.BadRequest("validation.invalid_request", "edition_id is invalid")
		}
		editionID = &id
	}
	if workID == nil && editionID == nil {
		return nil, nil, apperr.BadRequest("validation.invalid_request", "work_id or edition_id is required")
	}
	return workID, editionID, nil
}
