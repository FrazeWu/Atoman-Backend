package debate

import (
	"net/http"
	"strconv"

	"atoman/internal/modules/comment"
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
	group.GET("/debate/topics", h.listDebates)
	group.GET("/debate/topics/search", h.searchDebates)
	group.GET("/debate/topics/:debateID", h.getDebate)
	group.POST("/debate/topics", h.createLegacyDebate)
	group.PUT("/debate/topics/:debateID", h.updateLegacyDebate)
	group.DELETE("/debate/topics/:debateID", h.deleteLegacyDebate)
	group.POST("/debate/topics/:debateID/conclude", h.concludeLegacyDebate)
	group.POST("/debate/topics/:debateID/reopen", h.reopenLegacyDebate)
	group.POST("/debates", h.createDebate)
	group.GET("/debates/:debateID/arguments", h.listArguments)
	group.POST("/debates/:debateID/arguments", h.createLegacyArgument)
	group.PATCH("/debate-arguments/:argumentID", h.updateLegacyArgument)
	group.DELETE("/debate-arguments/:argumentID", h.deleteLegacyArgument)
	group.POST("/debate-arguments/:argumentID/reference", h.addArgumentReference)
	group.DELETE("/debate-arguments/:argumentID/reference/:referenceID", h.removeArgumentReference)
	group.POST("/debate-arguments/:argumentID/debate-reference", h.addDebateReference)
	group.DELETE("/debate-arguments/:argumentID/debate-reference/:debateRefID", h.removeDebateReference)
	group.POST("/debate-arguments/:argumentID/fold", h.foldArgument)
	group.DELETE("/debate-arguments/:argumentID/fold", h.unfoldArgument)
}

func (h *Handler) listDebates(c *gin.Context) {
	query := ListDebatesQuery{
		Status:   c.Query("status"),
		Tag:      c.Query("tag"),
		Page:     page(c),
		PageSize: pageSize(c),
	}
	debates, total, err := h.service.ListDebates(query)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.List(c, debates, query.Page, query.PageSize, total)
}

func (h *Handler) searchDebates(c *gin.Context) {
	query := ListDebatesQuery{
		Search:   c.Query("q"),
		Page:     page(c),
		PageSize: pageSize(c),
	}
	debates, total, err := h.service.ListDebates(query)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.List(c, debates, query.Page, query.PageSize, total)
}

func (h *Handler) getDebate(c *gin.Context) {
	debateID, err := uuid.Parse(c.Param("debateID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "debateID must be a valid uuid"))
		return
	}
	debate, err := h.service.GetDebate(debateID)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, debate)
}

// listArguments godoc
// @Summary List typed debate arguments
// @Tags Debate
// @Produce json
// @Param debateID path string true "Debate ID"
// @Param page query int false "Page"
// @Param page_size query int false "Page size"
// @Success 200 {object} handlers.DebateArgumentListResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/debates/{debateID}/arguments [get]
func (h *Handler) listArguments(c *gin.Context) {
	debateID, err := uuid.Parse(c.Param("debateID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "debateID must be a valid uuid"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	arguments, total, err := h.service.ListArguments(debateID, page, pageSize)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	userVotes := map[string]int{}
	if user, ok := authctx.Current(c); ok {
		userVotes, err = h.service.ListArgumentVotes(user.ID, debateID)
		if err != nil {
			httpx.Error(c, comment.AppError(err))
			return
		}
	}
	httpx.OKMeta(c, http.StatusOK, arguments, gin.H{
		"page": page, "page_size": pageSize, "total": total,
		"has_more": int64(page*pageSize) < total, "user_votes": userVotes,
	})
}

// createDebate godoc
// @Summary Create a debate
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreateDebateRequest true "Debate"
// @Success 201 {object} handlers.DebateResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Router /api/v1/debates [post]
func (h *Handler) createDebate(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateDebateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	debate, err := h.service.CreateDebate(user, req)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusCreated, debate)
}

func (h *Handler) createLegacyDebate(c *gin.Context) {
	h.createDebate(c)
}

func (h *Handler) updateLegacyDebate(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	debateID, err := uuid.Parse(c.Param("debateID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "debateID must be a valid uuid"))
		return
	}
	var req CreateDebateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	debate, err := h.service.UpdateDebate(user, debateID, req)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, debate)
}

func (h *Handler) deleteLegacyDebate(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	debateID, err := uuid.Parse(c.Param("debateID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "debateID must be a valid uuid"))
		return
	}
	if err := h.service.DeleteDebate(user, debateID); err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Debate deleted"})
}

// createLegacyArgument godoc
// @Summary Create a typed debate argument
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param debateID path string true "Debate ID"
// @Param body body CreateArgumentRequest true "Argument"
// @Success 201 {object} handlers.DebateArgumentResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/debates/{debateID}/arguments [post]
func (h *Handler) createLegacyArgument(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	debateID, err := uuid.Parse(c.Param("debateID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "debateID must be a valid uuid"))
		return
	}
	var req CreateArgumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	req.DebateID = debateID
	argument, err := h.service.CreateArgument(user, req)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusCreated, argument)
}

func (h *Handler) concludeLegacyDebate(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	debateID, err := uuid.Parse(c.Param("debateID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "debateID must be a valid uuid"))
		return
	}
	var req struct {
		ConclusionType    string `json:"conclusion_type"`
		ConclusionSummary string `json:"conclusion_summary"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	debate, err := h.service.ConcludeDebate(user, debateID, req.ConclusionType, req.ConclusionSummary)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, debate)
}

func (h *Handler) reopenLegacyDebate(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	debateID, err := uuid.Parse(c.Param("debateID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "debateID must be a valid uuid"))
		return
	}
	debate, err := h.service.ReopenDebate(user, debateID)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, debate)
}

// updateLegacyArgument godoc
// @Summary Update a typed debate argument
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param argumentID path string true "Argument ID"
// @Param body body CreateArgumentRequest true "Argument"
// @Success 200 {object} handlers.DebateArgumentResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/debate-arguments/{argumentID} [patch]
func (h *Handler) updateLegacyArgument(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	argumentID, err := uuid.Parse(c.Param("argumentID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "argumentID must be a valid uuid"))
		return
	}
	var req CreateArgumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	argument, err := h.service.UpdateArgument(user, argumentID, req)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, argument)
}

// deleteLegacyArgument godoc
// @Summary Delete a typed debate argument
// @Tags Debate
// @Produce json
// @Security BearerAuth
// @Param argumentID path string true "Argument ID"
// @Success 200 {object} handlers.MessageResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/debate-arguments/{argumentID} [delete]
func (h *Handler) deleteLegacyArgument(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	argumentID, err := uuid.Parse(c.Param("argumentID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "argumentID must be a valid uuid"))
		return
	}
	if err := h.service.DeleteArgument(user, argumentID); err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Argument deleted"})
}

// addArgumentReference godoc
// @Summary Add an argument reference
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param argumentID path string true "Argument ID"
// @Param body body ReferenceRequest true "Reference"
// @Success 200 {object} handlers.MessageResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Router /api/v1/debate-arguments/{argumentID}/reference [post]
func (h *Handler) addArgumentReference(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	argumentID, err := uuid.Parse(c.Param("argumentID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "argumentID must be a valid uuid"))
		return
	}
	var req ReferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	if err := h.service.AddArgumentReference(user, argumentID, req.ReferenceID); err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Reference added"})
}

// removeArgumentReference godoc
// @Summary Remove an argument reference
// @Tags Debate
// @Produce json
// @Security BearerAuth
// @Param argumentID path string true "Argument ID"
// @Param referenceID path string true "Referenced argument ID"
// @Success 200 {object} handlers.MessageResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Router /api/v1/debate-arguments/{argumentID}/reference/{referenceID} [delete]
func (h *Handler) removeArgumentReference(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	argumentID, err := uuid.Parse(c.Param("argumentID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "argumentID must be a valid uuid"))
		return
	}
	referenceID, err := uuid.Parse(c.Param("referenceID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "referenceID must be a valid uuid"))
		return
	}
	if err := h.service.RemoveArgumentReference(user, argumentID, referenceID); err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Reference removed"})
}

// addDebateReference godoc
// @Summary Add a debate reference
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param argumentID path string true "Argument ID"
// @Param body body DebateReferenceRequest true "Debate reference"
// @Success 200 {object} handlers.MessageResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Router /api/v1/debate-arguments/{argumentID}/debate-reference [post]
func (h *Handler) addDebateReference(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	argumentID, err := uuid.Parse(c.Param("argumentID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "argumentID must be a valid uuid"))
		return
	}
	var req DebateReferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	if err := h.service.AddDebateReference(user, argumentID, req.DebateID); err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Debate reference added"})
}

// removeDebateReference godoc
// @Summary Remove a debate reference
// @Tags Debate
// @Produce json
// @Security BearerAuth
// @Param argumentID path string true "Argument ID"
// @Param debateRefID path string true "Referenced debate ID"
// @Success 200 {object} handlers.MessageResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Router /api/v1/debate-arguments/{argumentID}/debate-reference/{debateRefID} [delete]
func (h *Handler) removeDebateReference(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	argumentID, err := uuid.Parse(c.Param("argumentID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "argumentID must be a valid uuid"))
		return
	}
	debateID, err := uuid.Parse(c.Param("debateRefID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "debateRefID must be a valid uuid"))
		return
	}
	if err := h.service.RemoveDebateReference(user, argumentID, debateID); err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Debate reference removed"})
}

// foldArgument godoc
// @Summary Fold a debate argument
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param argumentID path string true "Argument ID"
// @Param body body handlers.FoldArgumentInput false "Fold note"
// @Success 200 {object} handlers.MessageResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Router /api/v1/debate-arguments/{argumentID}/fold [post]
func (h *Handler) foldArgument(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	argumentID, err := uuid.Parse(c.Param("argumentID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "argumentID must be a valid uuid"))
		return
	}
	var req struct {
		FoldNote string `json:"fold_note"`
	}
	_ = c.ShouldBindJSON(&req)
	if err := h.service.FoldArgument(user, argumentID, req.FoldNote); err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "folded"})
}

// unfoldArgument godoc
// @Summary Unfold a debate argument
// @Tags Debate
// @Produce json
// @Security BearerAuth
// @Param argumentID path string true "Argument ID"
// @Success 200 {object} handlers.MessageResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Router /api/v1/debate-arguments/{argumentID}/fold [delete]
func (h *Handler) unfoldArgument(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	argumentID, err := uuid.Parse(c.Param("argumentID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "argumentID must be a valid uuid"))
		return
	}
	if err := h.service.UnfoldArgument(user, argumentID); err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "unfolded"})
}

func page(c *gin.Context) int {
	value, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || value < 1 {
		return 1
	}
	return value
}

func pageSize(c *gin.Context) int {
	value, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil || value < 1 {
		return 20
	}
	if value > 100 {
		return 100
	}
	return value
}
