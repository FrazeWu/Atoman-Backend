package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"context"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strings"
)

type SetBookResourceStatusInput struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (s *Service) SetBookResourceStatus(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, input SetBookResourceStatusInput) (model.BookAsset, error) {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) || user.ID == uuid.Nil {
		return model.BookAsset{}, apperr.Forbidden("books.moderation_required", "Moderator permission required")
	}
	if (input.Status != "active" && input.Status != "removed") || strings.TrimSpace(input.Reason) == "" {
		return model.BookAsset{}, apperr.BadRequest("books.invalid_resource_status", "Status and a reason required")
	}
	var asset model.BookAsset
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&asset, "id = ?", id).Error; err != nil {
			return publicBookNotFound(err)
		}
		if input.Status == "active" {
			if asset.CurrentRevisionID == nil {
				return apperr.Conflict("books.revision_not_ready", "No safe revision available")
			}
			var revision model.BookAssetRevision
			if err := tx.Where("id = ? AND status = 'ready'", *asset.CurrentRevisionID).First(&revision).Error; err != nil {
				return publicBookNotFound(err)
			}
		}
		if err := tx.Model(&asset).Update("status", input.Status).Error; err != nil {
			return err
		}
		asset.Status = input.Status
		_, err := appendBookWikiRevision(tx, "asset", asset.ID, user.ID, input.Reason, "moderation", map[string]any{"status": asset.Status, "work_id": asset.WorkID})
		return err
	})
	return asset, err
}

// setBookResourceStatus godoc
// @Summary 管理员下架或恢复电子书，保留不可变文件及私人阅读数据
// @Tags books-assets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "电子书 UUID"
// @Param input body SetBookResourceStatusInput true "状态及原因，需版主权限"
// @Success 200 {object} model.BookAsset
// @Failure 403 {object} handlers.ErrorResponse
// @Router /api/v1/books/assets/{assetId}/status [patch]
func (h *Handler) setBookResourceStatus(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "assetId")
	if !ok {
		return
	}
	var input SetBookResourceStatusInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid status"))
		return
	}
	result, err := h.service.SetBookResourceStatus(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
