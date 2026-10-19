package user_theme

import (
	"net/http"

	_ "atoman/internal/handlers"
	"atoman/internal/middleware"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Handler handles HTTP requests for user themes.
type Handler struct {
	service *Service
}

// NewHandler creates a new user theme HTTP handler instance.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func requireCurrentUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		if user, ok := authctx.Current(c); ok && user.ID != uuid.Nil {
			c.Next()
			return
		}
		middleware.AuthMiddleware()(c)
	}
}

// RegisterRoutes mounts user theme endpoints on the provided router group.
func RegisterRoutes(v1 *gin.RouterGroup, handler *Handler) {
	v1.GET("/users/:handle/theme", handler.GetByHandle)
	v1.PUT("/users/me/theme", requireCurrentUser(), handler.UpdateTheme)
	v1.DELETE("/users/me/theme", requireCurrentUser(), handler.ResetTheme)
}

// GetByHandle godoc
// @Summary 获取用户个性化主题
// @Description 获取指定用户的个性化主题配置。如果用户存在但未定制主题，返回默认主题配置。
// @Tags user_theme
// @Produce json
// @Param handle path string true "用户 Handle（用户名或 UUID）"
// @Success 200 {object} UserThemeDTO
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/users/{handle}/theme [get]
func (h *Handler) GetByHandle(c *gin.Context) {
	handle := c.Param("handle")
	if handle == "me" {
		if cur, ok := authctx.Current(c); ok && cur.ID != uuid.Nil {
			handle = cur.ID.String()
		}
	}

	theme, err := h.service.GetByHandle(c.Request.Context(), handle)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, theme)
}

// UpdateTheme godoc
// @Summary 更新当前用户的主题设置
// @Description 更新当前创作者的主题设置，支持预设模式与自定义 CSS。自定义 CSS 会自动进行安全过滤与作用域隔离。
// @Tags user_theme
// @Accept json
// @Produce json
// @Param input body UpdateThemeInput true "主题配置"
// @Success 200 {object} UserThemeDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/users/me/theme [put]
func (h *Handler) UpdateTheme(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	var input UpdateThemeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", err.Error()))
		return
	}

	theme, err := h.service.UpdateTheme(c.Request.Context(), user.ID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, theme)
}

// ResetTheme godoc
// @Summary 重置当前用户的主题设置
// @Description 重置并清除当前创作者的主题配置，恢复为系统默认样式。
// @Tags user_theme
// @Produce json
// @Success 200 {object} map[string]any
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/users/me/theme [delete]
func (h *Handler) ResetTheme(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	if err := h.service.ResetTheme(c.Request.Context(), user.ID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"reset": true})
}
