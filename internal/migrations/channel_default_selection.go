package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func RunChannelDefaultSelectionMigration(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&model.Channel{}, &model.UserDefaultChannel{}); err != nil {
			return err
		}

		if err := tx.Model(&model.Channel{}).
			Where("content_type IS NULL OR content_type = ''").
			Update("content_type", model.ChannelContentTypeBlog).Error; err != nil {
			return err
		}

		var channels []model.Channel
		if err := tx.
			Where("user_id IS NOT NULL AND is_default = ?", true).
			Order("created_at ASC").
			Find(&channels).Error; err != nil {
			return err
		}

		for _, channel := range channels {
			if channel.UserID == nil {
				continue
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "user_id"}, {Name: "content_type"}},
				DoNothing: true,
			}).Create(&model.UserDefaultChannel{
				UserID:      *channel.UserID,
				ContentType: model.ChannelContentTypeBlog,
				ChannelID:   channel.ID,
			}).Error; err != nil {
				return err
			}
		}

		return nil
	})
}
