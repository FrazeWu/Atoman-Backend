package middleware

import (
	"encoding/json"
	"sync"
	"time"

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

type cachedSiteAccessEntry struct {
	notFound  bool
	payload   siteAccessPayload
	expiresAt time.Time
}

var siteAccessCache sync.Map

const siteAccessCacheTTL = 5 * time.Second

func init() {
	model.RegisterSiteSettingChangeHook(InvalidateSiteAccessCache)
}

func InvalidateSiteAccessCache() {
	siteAccessCache.Range(func(key, _ any) bool {
		siteAccessCache.Delete(key)
		return true
	})
}

func loadCachedSiteAccess(db *gorm.DB) (cachedSiteAccessEntry, bool, error) {
	var poolKey any
	if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
		poolKey = sqlDB
		if raw, ok := siteAccessCache.Load(poolKey); ok {
			entry := raw.(cachedSiteAccessEntry)
			if time.Now().Before(entry.expiresAt) {
				return entry, true, nil
			}
			siteAccessCache.Delete(poolKey)
		}
	}

	var setting model.SiteSetting
	err := db.First(&setting, "key = ?", "site.module_access").Error
	if err == gorm.ErrRecordNotFound {
		entry := cachedSiteAccessEntry{notFound: true, expiresAt: time.Now().Add(siteAccessCacheTTL)}
		if poolKey != nil {
			siteAccessCache.Store(poolKey, entry)
		}
		return entry, true, nil
	}
	if err != nil {
		if !model.HasTableCached(db, &model.SiteSetting{}) {
			return cachedSiteAccessEntry{notFound: true}, true, nil
		}
		return cachedSiteAccessEntry{}, false, err
	}

	var payload siteAccessPayload
	if err := json.Unmarshal([]byte(setting.Value), &payload); err != nil {
		return cachedSiteAccessEntry{}, false, err
	}
	entry := cachedSiteAccessEntry{payload: payload, expiresAt: time.Now().Add(siteAccessCacheTTL)}
	if poolKey != nil {
		siteAccessCache.Store(poolKey, entry)
	}
	return entry, true, nil
}

func RequireSiteFeature(db *gorm.DB, module, feature string) gin.HandlerFunc {
	return func(c *gin.Context) {
		cached, ok, err := loadCachedSiteAccess(db)
		if err != nil {
			httpx.Error(c, apperr.Internal(err))
			c.Abort()
			return
		}
		if !ok || cached.notFound {
			c.Next()
			return
		}

		entry, exists := cached.payload.Modules[module]
		enabled := exists && siteAccessModuleEnabled(module, entry) && entry.Features[feature]
		if !enabled {
			httpx.Error(c, apperr.Forbidden("site.feature_disabled", "This publishing feature is not available"))
			c.Abort()
			return
		}
		c.Next()
	}
}

func siteAccessModuleEnabled(module string, entry siteAccessModule) bool {
	if entry.Enabled != nil {
		return *entry.Enabled
	}
	if entry.Visible != nil {
		return *entry.Visible
	}
	// Keep raw legacy payloads aligned with the service defaults. Modules that
	// are not explicitly enabled must not accidentally open a gated feature.
	return module != "books"
}
