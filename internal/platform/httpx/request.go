package httpx

import (
	"atoman/internal/platform/apperr"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func BindRequiredJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return false
	}
	return true
}

func UUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := ParseUUID(c.Param(name), name)
	if err != nil {
		Error(c, err)
		return uuid.Nil, false
	}
	return id, true
}

func ParseUUID(raw, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", name+" must be a valid uuid")
	}
	return id, nil
}
