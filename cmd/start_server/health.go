package main

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerHealthRoutes(r *gin.Engine, db *gorm.DB) {
	r.GET("/healthz", func(c *gin.Context) {
		httpx.OK(c, http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/readyz", func(c *gin.Context) {
		sqlDB, err := db.DB()
		if err != nil || sqlDB.PingContext(c.Request.Context()) != nil {
			httpx.Error(c, apperr.New(http.StatusServiceUnavailable, "system.not_ready", "Service is not ready", nil))
			return
		}
		httpx.OK(c, http.StatusOK, gin.H{"status": "ready"})
	})
}
