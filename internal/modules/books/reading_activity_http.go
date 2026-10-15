package books

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// recordBookReadingHeartbeat godoc
// @Summary 记录有效阅读心跳，只使用服务器时间，多设备不重复计时
// @Description 客户端建议每 15 秒发送；visible 表示前台，active 表示近期有人机交互。只计两次均有效且间隔不超过 30 秒的同一会话，长断线不补算；重复或乱序序号不计时。返回秒数为本次新增，重复响应为 0，不用于在客户端推算总量。
// @Tags books-reading
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param sessionId path string true "阅读会话 UUID"
// @Param input body BookReadingHeartbeatInput true "会话内递增序号、前台及活跃状态"
// @Success 200 {object} BookReadingHeartbeatResult
// @Router /api/v1/books/me/reading-sessions/{sessionId}/heartbeat [post]
func (h *Handler) recordBookReadingHeartbeat(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "sessionId")
	if !ok {
		return
	}
	var input BookReadingHeartbeatInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid reading heartbeat"))
		return
	}
	result, err := h.service.RecordBookReadingHeartbeat(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// getBookReadingStatistics godoc
// @Summary 获取本人按 UTC 日期、作品汇总的有效阅读秒数
// @Tags books-reading
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param from query string false "起始 UTC 日期 YYYY-MM-DD，默认截止前 29 天"
// @Param to query string false "截止 UTC 日期 YYYY-MM-DD，默认今天；含首尾，最多 366 天"
// @Success 200 {object} BookReadingStatistics
// @Router /api/v1/books/me/reading-statistics [get]
func (h *Handler) getBookReadingStatistics(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	result, err := h.service.GetBookReadingStatistics(c.Request.Context(), user, c.Query("from"), c.Query("to"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// publishBookPrivateNote godoc
// @Summary 明确确认后，将私人笔记当前版本复制为独立公开内容
// @Description 复用现有频道发布流程，只复制标题和正文，不复制私人标签、来源 ID 和阅读状态。私人笔记后续编辑、删除或过期不会修改公开副本；重复请求返回原内容，不会重新发布已撤回内容。
// @Tags books-notes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param noteId path string true "本人私人笔记 UUID"
// @Param input body PublishBookPrivateNoteInput true "请求 UUID、已确认笔记版本、本人频道和公开确认"
// @Success 200 {object} BookNotePublicationDTO
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/me/private-notes/{noteId}/publish [post]
func (h *Handler) publishBookPrivateNote(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "noteId")
	if !ok {
		return
	}
	var input PublishBookPrivateNoteInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid note publication"))
		return
	}
	result, err := h.service.PublishBookPrivateNote(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
