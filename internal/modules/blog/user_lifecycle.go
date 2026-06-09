package blog

import (
	"fmt"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const anonymizedChannelName = "已注销用户"

func (s *Service) AnonymizeUserChannels(userID uuid.UUID) error {
	if userID == uuid.Nil {
		return nil
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var channels []model.Channel
		if err := tx.Where("user_id = ?", userID).Order("created_at asc").Find(&channels).Error; err != nil {
			return err
		}
		for i, channel := range channels {
			name, err := s.uniqueAnonymizedChannelName(tx, i)
			if err != nil {
				return err
			}
			if err := tx.Model(&channel).Updates(map[string]any{
				"user_id":      nil,
				"is_anonymous": true,
				"name":         name,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) uniqueAnonymizedChannelName(tx *gorm.DB, index int) (string, error) {
	counter := index + 1
	for {
		candidate := anonymizedChannelName
		if counter > 1 {
			candidate = fmt.Sprintf("%s %d", anonymizedChannelName, counter)
		}
		var count int64
		if err := tx.Model(&model.Channel{}).Where("LOWER(name) = LOWER(?)", candidate).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
		counter++
	}
}
