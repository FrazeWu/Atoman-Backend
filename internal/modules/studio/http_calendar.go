package studio

import (
	"net/http"
	"strings"
	"time"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// listCalendar godoc
// @Summary 获取定时发布日历
// @Tags studio
// @Produce json
// @Param channel_id query string false "频道 UUID"
// @Param from query string true "浏览器本地范围起点的 RFC3339 时间"
// @Param to query string true "浏览器本地范围终点的 RFC3339 时间"
// @Success 200 {array} StudioCalendarItem
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/calendar [get]
func (h *Handler) listCalendar(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	channelID, ok := optionalUUIDQuery(c, "channel_id")
	if !ok {
		return
	}
	from, ok := calendarTimeQuery(c, "from")
	if !ok {
		return
	}
	to, ok := calendarTimeQuery(c, "to")
	if !ok {
		return
	}
	items, err := h.service.ListCalendar(user, StudioCalendarQuery{ChannelID: channelID, From: from, To: to})
	respond(c, http.StatusOK, items, err)
}

func calendarTimeQuery(c *gin.Context, name string) (time.Time, bool) {
	raw := strings.TrimSpace(c.Query(name))
	value, err := time.Parse(time.RFC3339, raw)
	if raw == "" || err != nil {
		httpx.Error(c, apperr.BadRequest("studio.invalid_calendar_range", name+" must be an RFC3339 time"))
		return time.Time{}, false
	}
	return value.UTC(), true
}
