package portal

import (
	"net/http"
	"strconv"

	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct {
	service *Service
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	handler := &Handler{service: service}
	group.GET("/hot", handler.HotContent)
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

func (h *Handler) HotContent(c *gin.Context) {
	limit := parseLimit(c.Query("limit"), 6)
	response, err := h.service.HotContent(limit)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, response)
}

func parseLimit(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	if value > 12 {
		return 12
	}
	return value
}
