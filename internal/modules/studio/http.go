package studio

import (
	"net/http"
	"strconv"

	"atoman/internal/middleware"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.Use(middleware.AuthMiddleware())
	group.GET("/state", h.getState)
	group.PATCH("/state", h.patchState)
	group.GET("/dashboard", h.getDashboard)
	group.GET("/channels", h.listChannels)
	group.POST("/channels", h.createChannel)
	group.PATCH("/channels/:id", h.updateChannel)
	group.DELETE("/channels/:id", h.deleteChannel)
	group.GET("/:module/contents", h.listContents)
	group.POST("/:module/contents/:id/share", h.shareContent)
	group.GET("/:module/analytics", h.getAnalytics)
	group.GET("/:module/interactions", h.listInteractions)
	group.GET("/:module/settings", h.getSettings)
	group.PATCH("/:module/settings", h.patchSettings)
	group.GET("/:module/collections", h.listCollections)
	group.POST("/:module/collections", h.createCollection)
	group.PATCH("/:module/collections/:id", h.updateCollection)
	group.DELETE("/:module/collections/:id", h.deleteCollection)
}

func (h *Handler) getAnalytics(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	channelID, ok := optionalUUIDQuery(c, "channel_id")
	if !ok {
		return
	}
	rangeDays := 0
	if raw := c.Query("range"); raw != "" {
		var err error
		rangeDays, err = strconv.Atoi(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("studio.invalid_analytics_range", "range must be 7, 28, or 90"))
			return
		}
	}
	analytics, err := h.service.GetAnalytics(user, module, AnalyticsQuery{ChannelID: channelID, Range: rangeDays})
	respond(c, http.StatusOK, analytics, err)
}

func (h *Handler) listInteractions(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	channelID, ok := optionalUUIDQuery(c, "channel_id")
	if !ok {
		return
	}
	unreplied, ok := optionalBoolQuery(c, "unreplied")
	if !ok {
		return
	}
	anchored, ok := optionalBoolQuery(c, "anchored")
	if !ok {
		return
	}
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.ListInteractions(user, module, InteractionQuery{
		ChannelID: channelID, Unreplied: unreplied, Anchored: anchored, Page: page, PageSize: pageSize,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, page, pageSize, total)
}

func (h *Handler) getSettings(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	channelID, ok := optionalUUIDQuery(c, "channel_id")
	if !ok {
		return
	}
	settings, err := h.service.GetSettings(user, module, channelID)
	respond(c, http.StatusOK, settings, err)
}

func (h *Handler) patchSettings(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	var input SettingsInput
	if !bindJSON(c, &input) {
		return
	}
	settings, err := h.service.SaveSettings(user, module, input)
	respond(c, http.StatusOK, settings, err)
}

func (h *Handler) shareContent(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	contentID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	channelID, ok := optionalUUIDQuery(c, "channel_id")
	if !ok {
		return
	}
	share, err := h.service.ShareContent(user, module, channelID, contentID)
	respond(c, http.StatusOK, share, err)
}

func (h *Handler) getDashboard(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	channelID, ok := optionalUUIDQuery(c, "channel_id")
	if !ok {
		return
	}
	dashboard, err := h.service.GetDashboard(user, channelID)
	respond(c, http.StatusOK, dashboard, err)
}

func (h *Handler) listContents(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	channelID, ok := optionalUUIDQuery(c, "channel_id")
	if !ok {
		return
	}
	collectionID, ok := optionalUUIDQuery(c, "collection_id")
	if !ok {
		return
	}
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.ListContents(user, module, ContentQuery{
		ChannelID: channelID, Search: c.Query("q"), Status: c.Query("status"),
		Visibility: c.Query("visibility"), CollectionID: collectionID,
		Page: page, PageSize: pageSize,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, page, pageSize, total)
}

func (h *Handler) getState(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	state, err := h.service.GetState(user)
	respond(c, http.StatusOK, state, err)
}

func (h *Handler) patchState(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	var input PutStateInput
	if !bindJSON(c, &input) {
		return
	}
	state, err := h.service.SetState(user, input.ChannelID)
	respond(c, http.StatusOK, state, err)
}

func (h *Handler) listChannels(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	channels, err := h.service.ListChannels(user)
	respond(c, http.StatusOK, channels, err)
}

func (h *Handler) createChannel(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	var input CreateChannelInput
	if !bindJSON(c, &input) {
		return
	}
	channel, err := h.service.CreateChannel(user, input)
	respond(c, http.StatusCreated, channel, err)
}

func (h *Handler) updateChannel(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var input UpdateChannelInput
	if !bindJSON(c, &input) {
		return
	}
	channel, err := h.service.UpdateChannel(user, id, input)
	respond(c, http.StatusOK, channel, err)
}

func (h *Handler) deleteChannel(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteChannel(user, id); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Channel deleted"})
}

func (h *Handler) listCollections(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	channelID, err := uuid.Parse(c.Query("channel_id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "channel_id must be a valid uuid"))
		return
	}
	collections, serviceErr := h.service.ListCollections(user, channelID, module)
	respond(c, http.StatusOK, collections, serviceErr)
}

func (h *Handler) createCollection(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	var input CreateCollectionInput
	if !bindJSON(c, &input) {
		return
	}
	collection, err := h.service.CreateCollection(user, module, input)
	respond(c, http.StatusCreated, collection, err)
}

func (h *Handler) updateCollection(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var input UpdateCollectionInput
	if !bindJSON(c, &input) {
		return
	}
	collection, err := h.service.UpdateCollection(user, module, id, input)
	respond(c, http.StatusOK, collection, err)
}

func (h *Handler) deleteCollection(c *gin.Context) {
	user, module, ok := requestScope(c)
	if !ok {
		return
	}
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteCollection(user, module, id); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Collection deleted"})
}

func requestScope(c *gin.Context) (authctx.CurrentUser, Module, bool) {
	user, ok := currentUser(c)
	if !ok {
		return authctx.CurrentUser{}, "", false
	}
	module, err := ParseModule(c.Param("module"))
	if err != nil {
		httpx.Error(c, err)
		return authctx.CurrentUser{}, "", false
	}
	return user, module, true
}

func currentUser(c *gin.Context) (authctx.CurrentUser, bool) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return authctx.CurrentUser{}, false
	}
	return user, true
}

func uuidParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", name+" must be a valid uuid"))
		return uuid.Nil, false
	}
	return id, true
}

func optionalUUIDQuery(c *gin.Context, name string) (uuid.UUID, bool) {
	raw := c.Query(name)
	if raw == "" {
		return uuid.Nil, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", name+" must be a valid uuid"))
		return uuid.Nil, false
	}
	return id, true
}

func optionalBoolQuery(c *gin.Context, name string) (bool, bool) {
	raw := c.Query(name)
	if raw == "" {
		return false, true
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", name+" must be true or false"))
		return false, false
	}
	return value, true
}

func bindJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return false
	}
	return true
}

func respond(c *gin.Context, status int, data any, err error) {
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, status, data)
}
