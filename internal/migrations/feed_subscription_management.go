package migrations

import (
	"fmt"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// RunFeedSubscriptionManagementMigration backfills stable user-owned ordering after AutoMigrate adds the fields.
func RunFeedSubscriptionManagementMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable("subscriptions") || !db.Migrator().HasTable("subscription_groups") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var groups []model.SubscriptionGroup
		if err := tx.Order("user_id ASC, position ASC, created_at ASC").Find(&groups).Error; err != nil {
			return fmt.Errorf("load subscription groups: %w", err)
		}
		groupPositions := make(map[uuid.UUID]int)
		for _, group := range groups {
			position := groupPositions[group.UserID]
			if group.Position != position {
				if err := tx.Model(&model.SubscriptionGroup{}).Where("id = ?", group.ID).Update("position", position).Error; err != nil {
					return fmt.Errorf("backfill group position: %w", err)
				}
			}
			groupPositions[group.UserID] = position + 1
		}

		var subscriptions []model.Subscription
		if err := tx.Order("user_id ASC, subscription_group_id ASC, position ASC, created_at ASC").Find(&subscriptions).Error; err != nil {
			return fmt.Errorf("load subscriptions: %w", err)
		}
		positions := make(map[string]int)
		for _, subscription := range subscriptions {
			groupID := ""
			if subscription.SubscriptionGroupID != nil {
				groupID = subscription.SubscriptionGroupID.String()
			}
			key := subscription.UserID.String() + ":" + groupID
			position := positions[key]
			if subscription.Position != position {
				if err := tx.Model(&model.Subscription{}).Where("id = ?", subscription.ID).Update("position", position).Error; err != nil {
					return fmt.Errorf("backfill subscription position: %w", err)
				}
			}
			positions[key] = position + 1
		}
		return nil
	})
}
