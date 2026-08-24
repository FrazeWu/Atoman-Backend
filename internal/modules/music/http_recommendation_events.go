package music

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// recordMusicRecommendationEvents godoc
// @Summary 记录音乐推荐反馈事件
// @Description 记录登录用户对音乐首页推荐的曝光、点击和播放反馈。
// @Tags music
// @Accept json
// @Param body body RecordMusicRecommendationEventsRequest true "音乐推荐反馈事件"
// @Success 204
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/recommendation-events [post]
func (h *Handler) recordMusicRecommendationEvents(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	var input RecordMusicRecommendationEventsRequest
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.RecordMusicRecommendationEvents(user, input); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
