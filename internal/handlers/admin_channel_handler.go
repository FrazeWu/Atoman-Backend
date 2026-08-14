package handlers

import (
	"errors"
	"net/http"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"atoman/internal/platform/requestmeta"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type transferChannelOwnershipInput struct {
	UserID string `json:"user_id" binding:"required"`
}

// transferChannelOwnership godoc
// @Summary 转移频道所有权
// @Tags admin-users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "频道 UUID"
// @Param input body transferChannelOwnershipInput true "新所有者"
// @Success 200 {object} model.Channel
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/admin/channels/{id}/owner [put]
func (handler *adminUserHandler) transferChannelOwnership(c *gin.Context) {
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil || channelID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("admin_channel.invalid_id", "频道 ID 无效"))
		return
	}
	var input transferChannelOwnershipInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("admin_channel.invalid_input", "请指定新所有者"))
		return
	}
	targetID, err := uuid.Parse(strings.TrimSpace(input.UserID))
	if err != nil || targetID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("admin_channel.invalid_owner", "新所有者 ID 无效"))
		return
	}

	actor, _ := authctx.Current(c)
	var channel model.Channel
	if err := handler.db.First(&channel, "id = ?", channelID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("studio.channel_not_found", "频道不存在"))
		} else {
			httpx.Error(c, apperr.Internal(err))
		}
		return
	}
	if channel.UserID == nil {
		httpx.Error(c, apperr.Conflict("admin_channel.owner_required", "频道没有可转移的所有者"))
		return
	}
	var source, target model.User
	if err := handler.db.First(&source, "uuid = ?", *channel.UserID).Error; err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	if err := handler.db.First(&target, "uuid = ?", targetID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("admin_user.not_found", "新所有者不存在"))
		} else {
			httpx.Error(c, apperr.Internal(err))
		}
		return
	}
	if !target.IsActive {
		httpx.Error(c, apperr.Conflict("admin_channel.owner_inactive", "新所有者账号未启用"))
		return
	}
	if source.UUID == target.UUID {
		httpx.Error(c, apperr.BadRequest("admin_channel.owner_unchanged", "新所有者与当前所有者相同"))
		return
	}
	if !canManageAdminUser(actor, source) || !canManageAdminUser(actor, target) {
		httpx.Error(c, apperr.Forbidden("admin_channel.transfer_forbidden", "不能转移该频道所有权"))
		return
	}

	if err := handler.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&channel).Update("user_id", target.UUID).Error; err != nil {
			return err
		}
		if tx.Migrator().HasTable(&model.StudioModuleSettings{}) {
			if err := tx.Model(&model.StudioModuleSettings{}).Where("channel_id = ?", channel.ID).Update("user_id", target.UUID).Error; err != nil {
				return err
			}
		}
		if tx.Migrator().HasTable(&model.UserStudioState{}) {
			if err := tx.Model(&model.UserStudioState{}).Where("user_id = ? AND channel_id = ?", source.UUID, channel.ID).Update("channel_id", nil).Error; err != nil {
				return err
			}
		}
		actorID := actor.ID
		return audit.Record(tx, audit.Entry{
			ActorID: &actorID, Action: "admin_channel.owner_transferred", EntityType: "channel", EntityID: &channel.ID,
			Metadata: map[string]any{
				"previous_owner_id": source.UUID.String(),
				"new_owner_id":      target.UUID.String(),
				"ip_address":        requestmeta.FromGin(c).IPAddress,
			},
		})
	}); err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	channel.UserID = &target.UUID
	httpx.OK(c, http.StatusOK, channel)
}
