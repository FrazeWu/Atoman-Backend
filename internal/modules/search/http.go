package search

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"atoman/internal/middleware"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
)

type Handler struct{ service *Service }

type SearchEnvelope struct {
	Data SearchResponse `json:"data"`
}
type CapabilitiesEnvelope struct {
	Data CapabilitiesResponse `json:"data"`
}

type ErrorEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	routes := group.Group("/search", middleware.OptionalAuthMiddleware(), func(c *gin.Context) {
		// 私人结果、计数和能力声明都不可进入代理或浏览器的共享缓存。
		c.Header("Cache-Control", "no-store")
		c.Writer.Header().Add("Vary", "Cookie, Authorization")
		c.Next()
	})
	routes.GET("", h.search)
	routes.GET("/capabilities", h.capabilities)
}

// search godoc
// @Summary 统一内容搜索
// @Description preview 每组 2 条，grouped 每组 6 条；list 必须选择单个 module/type。filter[key] 仅接受 capabilities 声明的字段，重复值为 OR、不同字段为 AND。已接入 books 的 work、author、book_list（仅 public）；作品支持 language、author_id、year_from、year_to，年份为 1–9999 的整数，匹配任一有效公开版本。rating_desc 复用信誉加权评分和 6 人门槛。现有搜索入口不受影响。
// @Tags search
// @Produce json
// @Param q query string false "关键词，最多 256 个 Unicode 字符"
// @Param scope query string false "范围；mine 需 Cookie 或 API Token 登录" Enums(public,mine) default(public)
// @Param module query string false "已注册模块"
// @Param type query []string false "内容类型，可重复" collectionFormat(multi)
// @Param view query string false "结果模式" Enums(preview,grouped,list) default(grouped)
// @Param sort query string false "已注册排序"
// @Param page query int false "list 页码，offset 不超过 10000" minimum(1) default(1)
// @Param page_size query int false "list 页大小" minimum(1) maximum(50) default(24)
// @Success 200 {object} SearchEnvelope
// @Failure 400 {object} ErrorEnvelope
// @Failure 401 {object} ErrorEnvelope
// @Failure 403 {object} ErrorEnvelope
// @Failure 503 {object} ErrorEnvelope
// @Router /api/v1/search [get]
func (h *Handler) search(c *gin.Context) {
	req, err := ParseSearchRequest(c)
	if err != nil {
		writeError(c, err)
		return
	}
	result, err := h.service.Search(c.Request.Context(), viewerFromContext(c), req)
	if err != nil {
		writeError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// capabilities godoc
// @Summary 查询已实现的统一搜索能力
// @Description 始终返回 scope 和 modules 数组；指定 module/type 时收窄结果，模块级 sorts/filters 是选中类型的交集。未注册能力和已停用模块不会出现在结果中。
// @Tags search
// @Produce json
// @Param scope query string false "范围；mine 需登录" Enums(public,mine) default(public)
// @Param module query string false "已注册模块"
// @Param type query []string false "内容类型，可重复" collectionFormat(multi)
// @Success 200 {object} CapabilitiesEnvelope
// @Failure 400 {object} ErrorEnvelope
// @Failure 401 {object} ErrorEnvelope
// @Failure 403 {object} ErrorEnvelope
// @Router /api/v1/search/capabilities [get]
func (h *Handler) capabilities(c *gin.Context) {
	for key := range c.Request.URL.Query() {
		if !slices.Contains([]string{"scope", "module", "type"}, key) {
			writeError(c, invalid(key, "Unknown capability parameter"))
			return
		}
	}
	req, err := ParseSearchRequest(c)
	if err != nil {
		writeError(c, err)
		return
	}
	result, err := h.service.Capabilities(c.Request.Context(), viewerFromContext(c), req)
	if err != nil {
		writeError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

func viewerFromContext(c *gin.Context) Viewer {
	viewer, _ := authctx.Current(c)
	return viewer
}

func writeError(c *gin.Context, err error) {
	if c.Request.Context().Err() != nil {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = unavailable()
	}
	httpx.Error(c, err)
}
