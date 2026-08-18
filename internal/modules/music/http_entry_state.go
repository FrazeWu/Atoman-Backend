package music

import (
	"net/http"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// createMusicEntryStateRequest godoc
// @Summary 申请改变音乐 Wiki 条目编辑状态
// @Description 登录用户可申请关闭、重新开发或解除锁定。
// @Tags music-entry-state
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param entityType path string true "artist, album, song"
// @Param entityId path string true "条目 UUID"
// @Param input body CreateMusicStateRequestInput true "状态请求"
// @Success 201 {object} model.MusicEntryStateRequest
// @Router /api/v1/music/entries/{entityType}/{entityId}/state-requests [post]
func (h *Handler) createMusicEntryStateRequest(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	entityType, entityID, err := parseMusicEntryRoute(c)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var input CreateMusicStateRequestInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	request, err := h.service.CreateMusicEntryStateRequest(user, entityType, entityID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, request)
}

// listMusicEntryStateRequests godoc
// @Summary 查询音乐 Wiki 状态请求
// @Tags music-entry-state
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param entity_type query string false "artist, album, song"
// @Param entity_id query string false "条目 UUID"
// @Param status query string false "请求状态"
// @Success 200 {array} model.MusicEntryStateRequest
// @Router /api/v1/music/state-requests [get]
func (h *Handler) listMusicEntryStateRequests(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	entityType := strings.TrimSpace(c.Query("entity_type"))
	if entityType != "" && !validMusicEntityType(entityType) {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "invalid entity_type"))
		return
	}
	entityID := uuid.Nil
	if raw := strings.TrimSpace(c.Query("entity_id")); raw != "" {
		var err error
		entityID, err = parseMusicID(raw, "entity_id")
		if err != nil {
			httpx.Error(c, err)
			return
		}
	}
	requests, err := h.service.ListMusicEntryStateRequests(user, entityType, entityID, strings.TrimSpace(c.Query("status")))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, requests)
}

// reviewMusicEntryStateRequest godoc
// @Summary 审批音乐 Wiki 状态请求
// @Tags music-entry-state
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param requestId path string true "请求 UUID"
// @Param input body ReviewMusicStateRequestInput true "审批决定"
// @Success 200 {object} model.MusicEntryStateRequest
// @Router /api/v1/music/state-requests/{requestId}/decision [post]
func (h *Handler) reviewMusicEntryStateRequest(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	requestID, err := parseMusicID(c.Param("requestId"), "requestId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var input ReviewMusicStateRequestInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	request, err := h.service.ReviewMusicEntryStateRequest(user, requestID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, request)
}

// cancelMusicEntryStateRequest godoc
// @Summary 取消音乐 Wiki 状态请求
// @Tags music-entry-state
// @Security BearerAuth
// @Security CookieAuth
// @Param requestId path string true "请求 UUID"
// @Success 204
// @Router /api/v1/music/state-requests/{requestId} [delete]
func (h *Handler) cancelMusicEntryStateRequest(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	requestID, err := parseMusicID(c.Param("requestId"), "requestId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.CancelMusicEntryStateRequest(user, requestID); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// emergencyMusicEntryState godoc
// @Summary 管理员紧急改变音乐 Wiki 编辑状态
// @Tags music-entry-state
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param entityType path string true "artist, album, song"
// @Param entityId path string true "条目 UUID"
// @Param input body EmergencyMusicStateInput true "目标状态和原因"
// @Success 204
// @Router /api/v1/music/entries/{entityType}/{entityId}/state/emergency [post]
func (h *Handler) emergencyMusicEntryState(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	entityType, entityID, err := parseMusicEntryRoute(c)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var input EmergencyMusicStateInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.EmergencyMusicEntryState(user, entityType, entityID, input); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func parseMusicEntryRoute(c *gin.Context) (string, uuid.UUID, error) {
	entityType := strings.TrimSpace(c.Param("entityType"))
	if !validMusicEntityType(entityType) {
		return "", uuid.Nil, apperr.BadRequest("validation.invalid_request", "invalid entity type")
	}
	entityID, err := parseMusicID(c.Param("entityId"), "entityId")
	return entityType, entityID, err
}

func validMusicEntityType(entityType string) bool {
	return entityType == "artist" || entityType == "album" || entityType == "song"
}
