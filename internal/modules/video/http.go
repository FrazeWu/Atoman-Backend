package video

import (
	"atoman/internal/handlers"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func RegisterRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	handlers.SetupVideoRoutes(router, db, s3Client)
}
