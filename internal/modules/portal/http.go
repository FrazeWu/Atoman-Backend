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
	return &Service{db: db, hotCache: make(map[int]hotCacheEntry)}
}

// HotContent godoc
// @Summary 获取门户焦点精选
// @Description 返回全站高质量内容候选池中的当前四项；featured_total 表示候选总数，spotlight_offset 按候选位置切换下一批。
// @Tags portal
// @Produce json
// @Param limit query int false "每个模块的候选数量" default(6)
// @Param spotlight_offset query int false "焦点精选候选偏移量" default(0)
// @Success 200 {object} HotResponse
// @Failure 500 {object} map[string]string
// @Router /api/v1/portal/hot [get]
func (h *Handler) HotContent(c *gin.Context) {
	limit := parseLimit(c.Query("limit"), 6)
	spotlightOffset := parseSpotlightOffset(c.Query("spotlight_offset"))
	response, err := h.service.HotContentAtOffset(limit, spotlightOffset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.Header("Cache-Control", "public, max-age=30, s-maxage=60, stale-while-revalidate=300")
	httpx.OK(c, http.StatusOK, response)
}

func parseSpotlightOffset(raw string) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0
	}
	return value
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
