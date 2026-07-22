package reference

import (
	"net/http"
	"strconv"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service *Service
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	handler := &Handler{service: service}
	group.GET("/references/search", handler.search)
	group.POST("/references/resolve", handler.resolve)
}

// search godoc
// @Summary 搜索引用目标
// @Tags references
// @Produce json
// @Param type query string true "引用类型"
// @Param q query string false "搜索关键词"
// @Param limit query int false "返回数量"
// @Success 200 {object} SearchResponse
// @Failure 400 {object} ErrorResponse
// @Router /api/v1/references/search [get]
func (h *Handler) search(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, err := h.service.Search(viewerFromContext(c), c.Query("type"), c.Query("q"), limit)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, items)
}

// resolve godoc
// @Summary 解析草稿中的引用
// @Tags references
// @Accept json
// @Produce json
// @Param body body resolveRequest true "草稿正文"
// @Success 200 {object} ResolveResponse
// @Failure 400 {object} ErrorResponse
// @Router /api/v1/references/resolve [post]
func (h *Handler) resolve(c *gin.Context) {
	var request resolveRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid request body"))
		return
	}
	items, err := h.service.ResolvePreview(viewerFromContext(c), request.Content)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, items)
}

type resolveRequest struct {
	Content string `json:"content"`
}

func viewerFromContext(c *gin.Context) Viewer {
	user, ok := authctx.Current(c)
	if !ok {
		return Viewer{}
	}
	return Viewer{UserID: user.ID}
}

func normalizedReferenceType(value string) string { return strings.TrimSpace(value) }
