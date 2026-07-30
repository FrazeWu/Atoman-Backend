package handlers

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"atoman/internal/service"
)

func GetPublicSiteAccessHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		matrix, err := service.NewSiteAccessService(db).PublicMatrix()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_load_failed"})
			return
		}
		c.JSON(http.StatusOK, matrix)
	}
}

// GetSiteAccessHandler godoc
// @Summary 获取站点模块访问设置
// @Description 返回后台模块开关与结构化模块设置，包括 feed/blog/forum 的设置矩阵。
// @Tags settings
// @Produce json
// @Success 200 {object} service.SiteAccessMatrix
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/settings/site-access [get]
func GetSiteAccessHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		matrix, err := service.NewSiteAccessService(db).Load()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_load_failed"})
			return
		}
		c.JSON(http.StatusOK, matrix)
	}
}

// UpdateSiteAccessHandler godoc
// @Summary 更新站点模块访问设置
// @Description 保存后台模块开关与结构化模块设置，包括 feed/blog/forum 的设置矩阵。
// @Tags settings
// @Accept json
// @Produce json
// @Param input body service.SiteAccessMatrixInput true "站点访问设置输入"
// @Success 200 {object} service.SiteAccessMatrix
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/settings/site-access [put]
func UpdateSiteAccessHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var payload service.SiteAccessMatrixInput
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_site_access_payload"})
			return
		}

		svc := service.NewSiteAccessService(db)
		if err := svc.SaveInput(payload); err != nil {
			if errors.Is(err, service.ErrInvalidSiteAccessPayload) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_site_access_payload"})
				return
			}
			if errors.Is(err, service.ErrSiteAccessConflict) {
				c.JSON(http.StatusConflict, gin.H{"error": "site_access_conflict"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_save_failed"})
			return
		}

		matrix, err := svc.Load()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_save_failed"})
			return
		}
		c.JSON(http.StatusOK, matrix)
	}
}

func UpdateLegacySiteAccessHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_site_access_payload"})
			return
		}

		svc := service.NewSiteAccessService(db)
		if err := svc.SaveLegacyPayload(body); err != nil {
			if errors.Is(err, service.ErrInvalidSiteAccessPayload) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_site_access_payload"})
				return
			}
			if errors.Is(err, service.ErrSiteAccessConflict) {
				c.JSON(http.StatusConflict, gin.H{"error": "site_access_conflict"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_save_failed"})
			return
		}

		matrix, err := svc.Load()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_save_failed"})
			return
		}
		c.JSON(http.StatusOK, matrix)
	}
}
