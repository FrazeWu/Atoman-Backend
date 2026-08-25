package service

import (
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	fullTextHostLeaseDuration = 2 * time.Minute
	fullTextHostRetryDelay    = 15 * time.Second
	fullTextHostMinInterval   = 0
)

type fullTextHostLease struct {
	host  string
	token string
}

func acquireFullTextHostLease(db *gorm.DB, rawURL string, now time.Time) (fullTextHostLease, bool, error) {
	host := fullTextDomainKey(rawURL)
	if strings.TrimSpace(host) == "" {
		return fullTextHostLease{}, true, nil
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FeedFullTextHost{Host: host}).Error; err != nil {
		return fullTextHostLease{}, false, err
	}

	token := uuid.NewString()
	result := db.Model(&model.FeedFullTextHost{}).
		Where("host = ?", host).
		Where("lease_until IS NULL OR lease_until <= ?", now).
		Where("next_allowed_at IS NULL OR next_allowed_at <= ?", now).
		Updates(map[string]any{
			"lease_token": token,
			"lease_until": now.Add(fullTextHostLeaseDuration),
		})
	if result.Error != nil {
		return fullTextHostLease{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return fullTextHostLease{}, false, nil
	}
	return fullTextHostLease{host: host, token: token}, true, nil
}

func releaseFullTextHostLease(db *gorm.DB, lease fullTextHostLease, now time.Time) error {
	if lease.host == "" || lease.token == "" {
		return nil
	}
	return db.Model(&model.FeedFullTextHost{}).
		Where("host = ? AND lease_token = ?", lease.host, lease.token).
		Updates(map[string]any{
			"lease_token":     "",
			"lease_until":     nil,
			"next_allowed_at": now.Add(fullTextHostMinInterval),
		}).Error
}

func deferFullTextHostClaim(db *gorm.DB, item model.FeedItem, now time.Time) error {
	return db.Model(&model.FeedItem{}).
		Where("id = ? AND full_text_status = ?", item.ID, FullTextStatusFetching).
		Updates(map[string]any{
			"full_text_status":          FullTextStatusRetry,
			"full_text_attempt_count":   gorm.Expr("GREATEST(full_text_attempt_count - 1, 0)"),
			"last_full_text_attempt_at": nil,
			"next_full_text_attempt_at": now.Add(fullTextHostRetryDelay),
		}).Error
}
