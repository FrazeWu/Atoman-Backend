package authlogin

import (
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/requestmeta"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const Retention = 90 * 24 * time.Hour

func Record(db *gorm.DB, userID uuid.UUID, sessionID *uuid.UUID, method, result, failureCode string, info requestmeta.Info) error {
	event := model.LoginEvent{
		UserID:      userID,
		SessionID:   sessionID,
		Method:      method,
		Result:      result,
		FailureCode: failureCode,
		IPAddress:   info.IPAddress,
		IPPrefix:    info.IPPrefix,
		CountryCode: info.CountryCode,
		Region:      info.Region,
		City:        info.City,
		UserAgent:   info.UserAgent,
	}
	if err := db.Create(&event).Error; err != nil {
		return err
	}
	return db.Where("created_at < ?", time.Now().UTC().Add(-Retention)).Delete(&model.LoginEvent{}).Error
}
