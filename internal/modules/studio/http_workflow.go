package studio

import (
	"net/http"

	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// listUnifiedCollectionCandidates godoc
// @Summary 搜索统一合集候选内容
// @Tags studio
// @Produce json
// @Param id path string true "合集 UUID"
// @Param q query string false "搜索内容标题"
// @Success 200 {array} StudioCollectionContentCandidate
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/collections/{id}/candidates [get]
func (h *Handler) listUnifiedCollectionCandidates(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	collectionID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	items, err := h.service.ListUnifiedCollectionCandidates(user, collectionID, c.Query("q"))
	respond(c, http.StatusOK, items, err)
}

// addUnifiedCollectionContent godoc
// @Summary 加入或移动统一合集成员
// @Tags studio
// @Produce json
// @Param id path string true "合集 UUID"
// @Param contentId path string true "规范内容 UUID"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/collections/{id}/contents/{contentId} [put]
func (h *Handler) addUnifiedCollectionContent(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	collectionID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	contentID, ok := uuidParam(c, "contentId")
	if !ok {
		return
	}
	respond(c, http.StatusOK, gin.H{"moved": true}, h.service.AddUnifiedCollectionContent(user, collectionID, contentID))
}

// removeUnifiedCollectionContent godoc
// @Summary 移除统一合集成员
// @Tags studio
// @Produce json
// @Param id path string true "合集 UUID"
// @Param contentId path string true "规范内容 UUID"
// @Success 200 {object} map[string]bool
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/collections/{id}/contents/{contentId} [delete]
func (h *Handler) removeUnifiedCollectionContent(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	collectionID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	contentID, ok := uuidParam(c, "contentId")
	if !ok {
		return
	}
	respond(c, http.StatusOK, gin.H{"removed": true}, h.service.RemoveUnifiedCollectionContent(user, collectionID, contentID))
}

// listReplyTemplates godoc
// @Summary 获取回复模板
// @Tags studio
// @Produce json
// @Param channel_id query string true "频道 UUID"
// @Success 200 {array} StudioReplyTemplate
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/reply-templates [get]
func (h *Handler) listReplyTemplates(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	channelID, ok := optionalUUIDQuery(c, "channel_id")
	if !ok {
		return
	}
	templates, err := h.service.ListReplyTemplates(user, channelID)
	respond(c, http.StatusOK, templates, err)
}

// createReplyTemplate godoc
// @Summary 创建回复模板
// @Tags studio
// @Accept json
// @Produce json
// @Param input body StudioReplyTemplateInput true "回复模板"
// @Success 201 {object} StudioReplyTemplate
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/reply-templates [post]
func (h *Handler) createReplyTemplate(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	var input StudioReplyTemplateInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	template, err := h.service.CreateReplyTemplate(user, input)
	respond(c, http.StatusCreated, template, err)
}

// deleteReplyTemplate godoc
// @Summary 删除回复模板
// @Tags studio
// @Produce json
// @Param id path string true "模板 UUID"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/reply-templates/{id} [delete]
func (h *Handler) deleteReplyTemplate(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteReplyTemplate(user, id); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

// updateInteractionState godoc
// @Summary 更新互动处理状态
// @Tags studio
// @Accept json
// @Produce json
// @Param module path string true "内容模块" Enums(blog,podcast,video)
// @Param id path string true "评论 UUID"
// @Param channel_id query string false "频道 UUID"
// @Param input body UpdateInteractionStateInput true "处理状态"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/{module}/interactions/{id}/state [put]
func (h *Handler) updateInteractionState(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	commentID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	channelID, ok := optionalUUIDQuery(c, "channel_id")
	if !ok {
		return
	}
	var input UpdateInteractionStateInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	respond(c, http.StatusOK, gin.H{"updated": true}, h.service.UpdateInteractionState(user, module, channelID, commentID, input))
}

// setInteractionsHandled godoc
// @Summary 批量更新互动处理状态
// @Tags studio
// @Accept json
// @Produce json
// @Param module path string true "内容模块" Enums(blog,podcast,video)
// @Param channel_id query string false "频道 UUID"
// @Param input body BatchInteractionStateInput true "批量处理状态"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/{module}/interactions/states [put]
func (h *Handler) setInteractionsHandled(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	channelID, ok := optionalUUIDQuery(c, "channel_id")
	if !ok {
		return
	}
	var input BatchInteractionStateInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	respond(c, http.StatusOK, gin.H{"updated": true}, h.service.SetInteractionsHandled(user, module, channelID, input.CommentIDs, input.Handled))
}
