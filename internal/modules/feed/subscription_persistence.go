package feed

import (
	"errors"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ensureSubscription creates the live subscription once and returns the
// existing row when another request has already created it.
func ensureSubscription(db *gorm.DB, desired model.Subscription) (model.Subscription, bool, error) {
	result := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "feed_source_id"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Name: "deleted_at"}, Value: nil},
		}},
		DoNothing: true,
	}).Create(&desired)
	if result.Error != nil {
		return model.Subscription{}, false, result.Error
	}
	if result.RowsAffected > 0 {
		return desired, true, nil
	}

	var existing model.Subscription
	if err := db.Where("user_id = ? AND feed_source_id = ?", desired.UserID, desired.FeedSourceID).First(&existing).Error; err != nil {
		return model.Subscription{}, false, err
	}
	return existing, false, nil
}

func getLiveSubscription(db *gorm.DB, userID, feedSourceID uuid.UUID) (model.Subscription, error) {
	var subscription model.Subscription
	err := db.Where("user_id = ? AND feed_source_id = ?", userID, feedSourceID).First(&subscription).Error
	return subscription, err
}

func isSubscriptionNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
