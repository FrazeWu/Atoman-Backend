package handlers

import (
	"errors"
	"net/http"

	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func SetupSiteRoutes(router *gin.Engine, db *gorm.DB) {
	group := router.Group("/api/v1/site")
	group.GET("/resolve/:handle", ResolveSiteHandle(db))
}

func ResolveSiteHandle(db *gorm.DB) gin.HandlerFunc {
	namespace := service.NewSiteNamespaceService(db)
	return func(c *gin.Context) {
		result, err := namespace.Resolve(c.Request.Context(), c.Param("handle"))
		if err != nil {
			status := http.StatusInternalServerError
			message := "Failed to resolve site handle"
			if errors.Is(err, service.ErrSiteHandleInvalid) {
				status = http.StatusBadRequest
				message = "Invalid site handle"
			}
			c.JSON(status, gin.H{"error": message})
			return
		}
		if result.Type == "unknown" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Site handle not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": result, "message": "ok"})
	}
}
