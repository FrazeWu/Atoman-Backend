package model

import (
	"sync"
	"time"

	"gorm.io/gorm"
)

// SiteSetting stores administrator-configurable key/value parameters.
// Keys are namespaced by module: "forum.xxx", "timeline.xxx", etc.
type SiteSetting struct {
	Key         string    `json:"key" gorm:"primaryKey"`
	Value       string    `json:"value" gorm:"not null"`
	Description string    `json:"description"`
	Revision    int       `json:"revision" gorm:"not null;default:1"`
	UpdatedAt   time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}

func (SiteSetting) TableName() string { return "site_settings" }

var (
	siteSettingHooksMu sync.RWMutex
	siteSettingHooks   []func()
)

func RegisterSiteSettingChangeHook(fn func()) {
	if fn == nil {
		return
	}
	siteSettingHooksMu.Lock()
	siteSettingHooks = append(siteSettingHooks, fn)
	siteSettingHooksMu.Unlock()
}

func NotifySiteSettingChanged() {
	siteSettingHooksMu.RLock()
	hooks := append([]func(){}, siteSettingHooks...)
	siteSettingHooksMu.RUnlock()
	for _, fn := range hooks {
		fn()
	}
}

func (SiteSetting) AfterSave(_ *gorm.DB) error {
	NotifySiteSettingChanged()
	return nil
}

func (SiteSetting) AfterDelete(_ *gorm.DB) error {
	NotifySiteSettingChanged()
	return nil
}
