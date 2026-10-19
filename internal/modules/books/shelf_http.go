package books

import (
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func bookWorkRouteUser(c *gin.Context) (authctx.CurrentUser, uuid.UUID, bool) {
	user, ok := currentBookUser(c)
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	workID, err := uuid.Parse(strings.TrimSpace(c.Param("workId")))
	if err != nil || workID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "workId must be a valid UUID"))
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	return user, workID, true
}
