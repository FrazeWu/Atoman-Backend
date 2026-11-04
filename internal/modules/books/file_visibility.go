package books

import (
	"context"
	"errors"
	"net/http"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A public catalog entry does not automatically make its file public.
func (s *Service) CanReadBookRevision(ctx context.Context, userID, revisionID uuid.UUID) error {
	revision, err := s.readableBookRevision(ctx, revisionID)
	if err != nil {
		return err
	}
	if userID != uuid.Nil && revision.CreatedBy == userID {
		return nil
	}
	var visibility model.BookFileVisibility
	err = s.db.WithContext(ctx).Where("revision_id = ?", revisionID).First(&visibility).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("books.file_private", "Book file is not publicly accessible")
	}
	if err != nil {
		return err
	}
	if !visibility.Public {
		return apperr.NotFound("books.file_private", "Book file is not publicly accessible")
	}
	return nil
}

func (s *Service) SetBookFileVisibility(ctx context.Context, user authctx.CurrentUser, revisionID uuid.UUID, makePublic, rightsConfirmed bool) (model.BookFileVisibility, error) {
	var result model.BookFileVisibility
	if user.ID == uuid.Nil {
		return result, apperr.Unauthorized("Login required")
	}
	if makePublic && !rightsConfirmed {
		return result, apperr.BadRequest("books.rights_confirmation_required", "Confirm permission to share this file publicly")
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var revision model.BookAssetRevision
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", revisionID, model.BookRevisionStatusReady).First(&revision).Error; err != nil {
			return publicBookNotFound(err)
		}
		if revision.CreatedBy != user.ID {
			return apperr.Forbidden("books.file_owner_required", "Only the file uploader can change its access")
		}
		result = model.BookFileVisibility{RevisionID: revision.ID, Public: makePublic, UpdatedBy: user.ID, UpdatedAt: time.Now().UTC()}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "revision_id"}}, DoUpdates: clause.AssignmentColumns([]string{"public", "updated_by", "updated_at"})}).Create(&result).Error
	})
	return result, err
}

func (h *Handler) setBookFileVisibility(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	var input struct {
		Public          bool `json:"public"`
		RightsConfirmed bool `json:"rights_confirmed"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid file sharing settings"))
		return
	}
	result, err := h.service.SetBookFileVisibility(c.Request.Context(), user, id, input.Public, input.RightsConfirmed)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
