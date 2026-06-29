package handlers

import (
	"net/http"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
)

func abortStorageUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"code":  "storage.unavailable",
		"error": "Storage service is unavailable",
	})
}

func requireS3(c *gin.Context, s3Client *s3.S3) bool {
	if s3Client != nil {
		return true
	}
	abortStorageUnavailable(c)
	return false
}
