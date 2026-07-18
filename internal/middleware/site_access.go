package middleware

import (
	"encoding/json"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type siteAccessModule struct {
	Enabled  *bool           `json:"enabled"`
	Visible  *bool           `json:"visible"`
	Features map[string]bool `json:"features"`
}

type siteAccessPayload struct {
	Modules map[string]siteAccessModule `json:"modules"`
}

func RequireSiteFeature(db *gorm.DB, module, feature string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !db.Migrator().HasTable(&model.SiteSetting{}) {
			c.Next()
			return
		}

		var setting model.SiteSetting
		err := db.First(&setting, "key = ?", "site.module_access").Error
		if err == gorm.ErrRecordNotFound {
			c.Next()
			return
		}
		if err != nil {
			httpx.Error(c, apperr.Internal(err))
			c.Abort()
			return
		}

		var payload siteAccessPayload
		if err := json.Unmarshal([]byte(setting.Value), &payload); err != nil {
			httpx.Error(c, apperr.Internal(err))
			c.Abort()
			return
		}
		entry, ok := payload.Modules[module]
		enabled := ok && (entry.Enabled == nil || *entry.Enabled) && (entry.Visible == nil || *entry.Visible) && entry.Features[feature]
		if !enabled {
			httpx.Error(c, apperr.Forbidden("site.feature_disabled", "This publishing feature is not available"))
			c.Abort()
			return
		}
		c.Next()
	}
}
