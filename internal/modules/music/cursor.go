package music

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type musicCreatedAtCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        uuid.UUID `json:"id"`
}

type musicCursorMeta struct {
	PageSize   int    `json:"page_size"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func parseMusicCreatedAtCursor(raw string) (*musicCreatedAtCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", "cursor is invalid")
	}
	var cursor musicCreatedAtCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.CreatedAt.IsZero() || cursor.ID == uuid.Nil {
		return nil, apperr.BadRequest("validation.invalid_request", "cursor is invalid")
	}
	return &cursor, nil
}

func encodeMusicCreatedAtCursor(createdAt time.Time, id uuid.UUID) string {
	payload, err := json.Marshal(musicCreatedAtCursor{CreatedAt: createdAt.UTC(), ID: id})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func writeMusicCursorList(c *gin.Context, data any, pageSize int, hasMore bool, nextCursor string) {
	httpx.OKMeta(c, 200, data, musicCursorMeta{PageSize: pageSize, HasMore: hasMore, NextCursor: nextCursor})
}
