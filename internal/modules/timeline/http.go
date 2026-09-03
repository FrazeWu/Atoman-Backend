package timeline

import (
	"atoman/internal/handlers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func RegisterRoutes(router *gin.Engine, db *gorm.DB) {
	handlers.SetupTimelineRoutes(router, db)
}
