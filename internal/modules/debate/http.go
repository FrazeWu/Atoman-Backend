package debate

import (
	"net/http"
	"strconv"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct{ service *Service }

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	topics := group.Group("/debate/topics")
	topics.GET("", h.list)
	topics.POST("", h.create)
	topics.GET("/:id", h.get)
	topics.PUT("/:id", h.save)
	topics.POST("/:id/archive", h.archive)
	topics.GET("/:id/revisions", h.listRevisions)
	topics.GET("/:id/revisions/:revisionID", h.getRevision)
	topics.GET("/:id/revisions/:revisionID/diff", h.diffRevision)
	topics.POST("/:id/revisions/:revisionID/revert", h.revertRevision)
	topics.POST("/:id/references/:relationID/reconfirm", h.reconfirm)
	topics.PUT("/:id/protection", h.putProtection)
	topics.DELETE("/:id/protection", h.deleteProtection)
	group.GET("/debates/:id/relations", h.graph)
}

// list godoc
// @Summary List debate topics
// @Tags Debate
// @Produce json
// @Param status query string false "Status"
// @Param search query string false "Search"
// @Param tag query string false "Tag"
// @Success 200 {object} handlers.DebateListResponse
// @Router /api/v1/debate/topics [get]
func (h *Handler) list(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.ListDebates(ListDebatesQuery{
		Status: c.Query("status"), Search: c.Query("search"), Tag: c.Query("tag"), Page: page, PageSize: pageSize,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, page, pageSize, total)
}

// create godoc
// @Summary Create a debate topic
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreateDebateRequest true "Debate"
// @Success 201 {object} handlers.DebateResponse
// @Router /api/v1/debate/topics [post]
func (h *Handler) create(c *gin.Context) {
	user, ok := requireCurrentUser(c)
	if !ok {
		return
	}
	var req CreateDebateRequest
	if !bindJSON(c, &req) {
		return
	}
	created, err := h.service.CreateDebate(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, created)
}

// get godoc
// @Summary Get a debate topic
// @Tags Debate
// @Produce json
// @Param id path string true "Debate ID"
// @Success 200 {object} handlers.DebateResponse
// @Router /api/v1/debate/topics/{id} [get]
func (h *Handler) get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	item, err := h.service.GetDebate(id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, item)
}

// save godoc
// @Summary Save a debate wiki revision
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Debate ID"
// @Param body body SaveWikiRequest true "Revision"
// @Success 200 {object} handlers.DebateResponse
// @Router /api/v1/debate/topics/{id} [put]
func (h *Handler) save(c *gin.Context) {
	user, ok := requireCurrentUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req SaveWikiRequest
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.service.SaveWiki(user, id, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, item)
}

// archive godoc
// @Summary Archive a debate topic
// @Tags Debate
// @Produce json
// @Security BearerAuth
// @Param id path string true "Debate ID"
// @Success 200 {object} handlers.DebateResponse
// @Router /api/v1/debate/topics/{id}/archive [post]
func (h *Handler) archive(c *gin.Context) {
	user, ok := requireCurrentUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	item, err := h.service.ArchiveDebate(user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, item)
}

// listRevisions godoc
// @Summary List debate revisions
// @Tags Debate
// @Produce json
// @Param id path string true "Debate ID"
// @Success 200 {object} handlers.DebateRevisionListResponse
// @Router /api/v1/debate/topics/{id}/revisions [get]
func (h *Handler) listRevisions(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	items, err := h.service.ListRevisions(id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, items)
}

// getRevision godoc
// @Summary Get a debate revision
// @Tags Debate
// @Produce json
// @Param id path string true "Debate ID"
// @Param revisionID path string true "Revision ID"
// @Success 200 {object} handlers.DebateRevisionResponse
// @Router /api/v1/debate/topics/{id}/revisions/{revisionID} [get]
func (h *Handler) getRevision(c *gin.Context) {
	id, revisionID, ok := parseTwoIDs(c, "id", "revisionID")
	if !ok {
		return
	}
	item, err := h.service.GetRevision(id, revisionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, item)
}

// diffRevision godoc
// @Summary Diff two debate revisions
// @Tags Debate
// @Produce json
// @Param id path string true "Debate ID"
// @Param revisionID path string true "Revision ID"
// @Param against query string true "Other revision ID"
// @Success 200 {object} handlers.DebateRevisionDiffResponse
// @Router /api/v1/debate/topics/{id}/revisions/{revisionID}/diff [get]
func (h *Handler) diffRevision(c *gin.Context) {
	id, revisionID, ok := parseTwoIDs(c, "id", "revisionID")
	if !ok {
		return
	}
	against, err := uuid.Parse(c.Query("against"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "against must be a valid uuid"))
		return
	}
	item, err := h.service.DiffRevisions(id, revisionID, against)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, item)
}

// revertRevision godoc
// @Summary Revert to a debate revision
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Debate ID"
// @Param revisionID path string true "Revision ID"
// @Param body body RevertRevisionRequest true "Revert"
// @Success 200 {object} handlers.DebateResponse
// @Router /api/v1/debate/topics/{id}/revisions/{revisionID}/revert [post]
func (h *Handler) revertRevision(c *gin.Context) {
	user, ok := requireCurrentUser(c)
	if !ok {
		return
	}
	id, revisionID, ok := parseTwoIDs(c, "id", "revisionID")
	if !ok {
		return
	}
	var req RevertRevisionRequest
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.service.RevertRevision(user, id, revisionID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, item)
}

// reconfirm godoc
// @Summary Reconfirm a stale debate reference
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Debate ID"
// @Param relationID path string true "Relation ID"
// @Param body body ReconfirmReferenceRequest true "Reconfirmation"
// @Success 200 {object} handlers.DebateResponse
// @Router /api/v1/debate/topics/{id}/references/{relationID}/reconfirm [post]
func (h *Handler) reconfirm(c *gin.Context) {
	user, ok := requireCurrentUser(c)
	if !ok {
		return
	}
	id, relationID, ok := parseTwoIDs(c, "id", "relationID")
	if !ok {
		return
	}
	var req ReconfirmReferenceRequest
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.service.ReconfirmReference(user, id, relationID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, item)
}

// putProtection godoc
// @Summary Protect a debate topic
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Debate ID"
// @Param body body ProtectionRequest true "Protection"
// @Success 200 {object} handlers.DebateMessageResponse
// @Router /api/v1/debate/topics/{id}/protection [put]
func (h *Handler) putProtection(c *gin.Context) {
	user, ok := requireCurrentUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req ProtectionRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := h.service.SetProtection(user, id, req); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Protection updated"})
}

// deleteProtection godoc
// @Summary Remove debate protection
// @Tags Debate
// @Produce json
// @Security BearerAuth
// @Param id path string true "Debate ID"
// @Success 200 {object} handlers.DebateMessageResponse
// @Router /api/v1/debate/topics/{id}/protection [delete]
func (h *Handler) deleteProtection(c *gin.Context) {
	user, ok := requireCurrentUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteProtection(user, id); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Protection removed"})
}

// graph godoc
// @Summary Get the read-only debate graph
// @Tags Debate
// @Produce json
// @Param id path string true "Debate ID"
// @Param view query string false "tree or graph"
// @Param depth query int false "Depth"
// @Success 200 {object} handlers.DebateGraphResponse
// @Router /api/v1/debates/{id}/relations [get]
func (h *Handler) graph(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	depth := 0
	if raw := c.Query("depth"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "depth must be a positive integer"))
			return
		}
		depth = parsed
	}
	graph, err := h.service.GetDebateGraph(id, c.Query("view"), depth)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, graph)
}

func requireCurrentUser(c *gin.Context) (authctx.CurrentUser, bool) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
	}
	return user, ok
}

func parseID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", name+" must be a valid uuid"))
		return uuid.Nil, false
	}
	return id, true
}

func parseTwoIDs(c *gin.Context, first, second string) (uuid.UUID, uuid.UUID, bool) {
	firstID, ok := parseID(c, first)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	secondID, ok := parseID(c, second)
	return firstID, secondID, ok
}

func bindJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return false
	}
	return true
}
