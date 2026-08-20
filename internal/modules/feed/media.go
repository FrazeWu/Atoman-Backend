package feed

import (
	"errors"
	"net/http"

	"atoman/internal/service"

	"github.com/gin-gonic/gin"
)

// proxyFeedImage godoc
// @Summary 加载订阅正文图片
// @Description 代理经过服务端签名的远程正文图片；未配置图片代理时不可用。
// @Tags feed
// @Produce image/png,image/jpeg,image/webp,image/gif
// @Param url query string true "远程图片 URL"
// @Param sig query string true "服务端签名"
// @Success 200 {file} binary
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /api/v1/feed/media/image [get]
func proxyFeedImage(c *gin.Context) {
	body, contentType, err := service.FetchFeedImageProxy(c.Query("url"), c.Query("sig"))
	if err != nil {
		switch {
		case errors.Is(err, service.ErrFeedImageProxyDisabled):
			c.JSON(http.StatusNotFound, gin.H{"error": "Image proxy is unavailable"})
		case errors.Is(err, service.ErrFeedImageProxySignature):
			c.JSON(http.StatusForbidden, gin.H{"error": "Invalid image signature"})
		default:
			c.JSON(http.StatusBadGateway, gin.H{"error": "Image is unavailable"})
		}
		return
	}
	c.Header("Cache-Control", "public, max-age=86400, s-maxage=604800, stale-while-revalidate=604800")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, body)
}
