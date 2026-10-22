package blog

import (
	"net/http"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *Handler) listChannels(c *gin.Context) {
	var userID *uuid.UUID
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "user_id must be a valid uuid"))
			return
		}
		userID = &parsed
	}
	channels, err := h.service.ListChannels(userID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channels)
}

// getChannel godoc
// @Summary GET /blog/channels/{id}
// @Tags blog
// @Success 200 {object} map[string]interface{}
// @Failure 401 "Unauthorized"
// @Router /api/v1/blog/channels/{id} [get]
func (h *Handler) getChannel(c *gin.Context) {
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	channel, err := h.service.GetChannel(channelID)
	if err != nil {
		// getChannelCollections godoc
		// @Summary GET /blog/channels/{id}/collections
		// @Tags blog
		// @Success 200 {object} map[string]interface{}
		// @Failure 401 "Unauthorized"
		// @Router /api/v1/blog/channels/{id}/collections [get]
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

func (h *Handler) getChannelCollections(c *gin.Context) {
	channelID, err := uuid.Parse(c.Param("id"))
	// getChannelBySlug godoc
	// @Summary GET /blog/channels/slug/{slug}
	// @Tags blog
	// @Success 200 {object} map[string]interface{}
	// @Failure 401 "Unauthorized"
	// @Router /api/v1/blog/channels/slug/{slug} [get]
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
		// getChannelCollectionsBySlug godoc
		// @Summary GET /blog/channels/slug/{slug}/collections
		// @Tags blog
		// @Success 200 {object} map[string]interface{}
		// @Failure 401 "Unauthorized"
		// @Router /api/v1/blog/channels/slug/{slug}/collections [get]
	}
	collections, err := h.service.ListCollectionsByChannel(channelID)
	if err != nil {
		// getCollection godoc
		// @Summary GET /blog/collections/{id}
		// @Tags blog
		// @Success 200 {object} map[string]interface{}
		// @Failure 401 "Unauthorized"
		// @Router /api/v1/blog/collections/{id} [get]
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collections)
}

func (h *Handler) getChannelBySlug(c *gin.Context) {
	channel, err := h.service.GetChannelBySlug(c.Param("slug"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

// listUserCollections godoc
// @Summary GET /blog/collections
// @Tags blog
// @Success 200 {object} map[string]interface{}
// @Failure 401 "Unauthorized"
// @Router /api/v1/blog/collections [get]

func (h *Handler) getChannelCollectionsBySlug(c *gin.Context) {
	// ensureDefaultChannel godoc
	// @Summary POST /blog/channels/ensure-default
	// @Tags blog
	// @Success 200 {object} map[string]interface{}
	// @Failure 401 "Unauthorized"
	// @Router /api/v1/blog/channels/ensure-default [post]
	_, collections, err := h.service.ListCollectionsByChannelSlug(c.Param("slug"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collections)
}

func (h *Handler) getCollection(c *gin.Context) {
	collectionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	collection, err := h.service.GetCollection(collectionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collection)
}

func (h *Handler) listUserCollections(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	collections, err := h.service.ListUserCollections(user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collections)
}

func (h *Handler) ensureDefaultChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channel, err := h.service.CreateDefaultChannelForUser(user.ID, user.Username)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

func (h *Handler) createChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req channelInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	channel, err := h.service.CreateChannel(user, req.Name, req.Slug, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, channel)
}

func (h *Handler) updateChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	var req channelInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	channel, err := h.service.UpdateChannel(user, channelID, req.Name, req.Slug, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

func (h *Handler) deleteChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	if err := h.service.DeleteChannel(user, channelID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Channel deleted"})
}

func (h *Handler) createCollection(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	var req collectionInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	collection, err := h.service.CreateCollection(user, channelID, req.Name, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, collection)
}

func (h *Handler) updateCollection(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	collectionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	var req collectionInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	collection, err := h.service.UpdateCollection(user, collectionID, req.Name, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collection)
}

func (h *Handler) deleteCollection(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	collectionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	if err := h.service.DeleteCollection(user, collectionID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Collection deleted"})
}
