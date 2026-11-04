package books

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

func (h *Handler) checkBookWorkDuplicates(c *gin.Context) {
	var input CheckBookWorkDuplicatesInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid work metadata"))
		return
	}
	matches, err := h.service.CheckBookWorkDuplicates(c.Request.Context(), input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"matches": matches})
}

func (h *Handler) checkBookEditionDuplicates(c *gin.Context) {
	_, id, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	var input CreateBookEditionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid edition metadata"))
		return
	}
	matches, err := h.service.CheckBookEditionDuplicates(c.Request.Context(), id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"matches": matches})
}
