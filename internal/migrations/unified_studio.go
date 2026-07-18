package migrations

import (
	"errors"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type studioChannelOwner struct {
	UserID uuid.UUID
}

type studioChannelSelection struct {
	ChannelID uuid.UUID
}

func RunUnifiedStudioMigration(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(
			&model.Collection{},
			&model.UserStudioState{},
			&model.StudioModuleSettings{},
		); err != nil {
			return err
		}

		if tx.Migrator().HasColumn(&model.Channel{}, "content_type") {
			if err := tx.Exec(`
				UPDATE collections
				SET content_type = COALESCE(NULLIF((
					SELECT channels.content_type
					FROM channels
					WHERE channels.id = collections.channel_id
				), ''), 'blog')
			`).Error; err != nil {
				return err
			}
		}

		if tx.Migrator().HasIndex(&model.Collection{}, "idx_collection_channel_name") {
			if err := tx.Migrator().DropIndex(&model.Collection{}, "idx_collection_channel_name"); err != nil {
				return err
			}
		}
		if err := tx.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS idx_collections_default_channel_type
			ON collections (channel_id, content_type)
			WHERE is_default = true AND deleted_at IS NULL
		`).Error; err != nil {
			return err
		}

		if !tx.Migrator().HasTable(&model.Channel{}) {
			return nil
		}
		var owners []studioChannelOwner
		if err := tx.Model(&model.Channel{}).
			Distinct("user_id").
			Where("user_id IS NOT NULL").
			Scan(&owners).Error; err != nil {
			return err
		}

		for _, owner := range owners {
			var existing model.UserStudioState
			err := tx.First(&existing, "user_id = ?", owner.UserID).Error
			if err == nil {
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}

			channelID, err := initialStudioChannelID(tx, owner.UserID)
			if err != nil {
				return err
			}
			if channelID == uuid.Nil {
				continue
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.UserStudioState{
				UserID: owner.UserID, ChannelID: &channelID,
			}).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

func initialStudioChannelID(tx *gorm.DB, userID uuid.UUID) (uuid.UUID, error) {
	if tx.Migrator().HasTable(&model.UserDefaultChannel{}) {
		var selection studioChannelSelection
		err := tx.Table("user_default_channels AS selections").
			Select("selections.channel_id").
			Joins("JOIN channels ON channels.id = selections.channel_id AND channels.deleted_at IS NULL").
			Where("selections.user_id = ?", userID).
			Order(`CASE selections.content_type
				WHEN 'blog' THEN 1
				WHEN 'podcast' THEN 2
				WHEN 'video' THEN 3
				ELSE 4 END`).
			First(&selection).Error
		if err == nil {
			return selection.ChannelID, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, err
		}
	}

	var channel model.Channel
	err := tx.Where("user_id = ?", userID).Order("created_at ASC").First(&channel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, err
	}
	return channel.ID, nil
}
