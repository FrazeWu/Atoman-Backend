package books

import (
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func catalogIdempotencyRequestID(c *gin.Context) (uuid.UUID, bool) {
	raw := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if raw == "" {
		return uuid.Nil, true // Backwards compatible with existing clients.
	}
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("books.invalid_request_id", "Idempotency-Key must be a non-zero UUID"))
		return uuid.Nil, false
	}
	return id, true
}
