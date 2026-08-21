package studio

import (
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) CreateUnifiedCollection(user authctx.CurrentUser, input CreateCollectionInput) (model.ContentCollection, error) {
	if err := requireUser(user); err != nil {
		return model.ContentCollection{}, err
	}
	if _, err := s.ownedChannel(user.ID, input.ChannelID); err != nil {
		return model.ContentCollection{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return model.ContentCollection{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	collection := model.ContentCollection{ChannelID: input.ChannelID, CreatedBy: &user.ID, Name: name, Description: strings.TrimSpace(input.Description), CoverURL: strings.TrimSpace(input.CoverURL)}
	if err := s.db.Create(&collection).Error; err != nil {
		return model.ContentCollection{}, err
	}
	return collection, nil
}

func (s *Service) UpdateUnifiedCollection(user authctx.CurrentUser, id uuid.UUID, input UpdateCollectionInput) (model.ContentCollection, error) {
	if err := requireUser(user); err != nil {
		return model.ContentCollection{}, err
	}
	var collection model.ContentCollection
	if err := s.db.First(&collection, "id = ?", id).Error; err != nil {
		return model.ContentCollection{}, err
	}
	if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
		return model.ContentCollection{}, err
	}
	updates := map[string]any{}
	if input.Name != nil {
		if name := strings.TrimSpace(*input.Name); name == "" {
			return model.ContentCollection{}, apperr.BadRequest("validation.invalid_request", "name is required")
		} else {
			updates["name"] = name
		}
	}
	if input.Description != nil {
		updates["description"] = strings.TrimSpace(*input.Description)
	}
	if input.CoverURL != nil {
		updates["cover_url"] = strings.TrimSpace(*input.CoverURL)
	}
	if len(updates) > 0 {
		if err := s.db.Model(&collection).Updates(updates).Error; err != nil {
			return model.ContentCollection{}, err
		}
	}
	return collection, nil
}

func (s *Service) ReorderUnifiedCollectionContents(user authctx.CurrentUser, collectionID uuid.UUID, contentIDs []uuid.UUID) error {
	if err := requireUser(user); err != nil {
		return err
	}
	var collection model.ContentCollection
	if err := s.db.First(&collection, "id = ?", collectionID).Error; err != nil {
		return err
	}
	if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
		return err
	}
	if len(contentIDs) == 0 {
		return nil
	}
	var count int64
	if err := s.db.Model(&model.ContentCollectionMembership{}).Where("collection_id = ? AND content_id IN ?", collectionID, contentIDs).Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(contentIDs)) {
		return apperr.BadRequest("studio.invalid_collection_contents", "content_ids must belong to the collection")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		for position, contentID := range contentIDs {
			if err := tx.Model(&model.ContentCollectionMembership{}).Where("collection_id = ? AND content_id = ?", collectionID, contentID).Update("position", position).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) DeleteUnifiedCollection(user authctx.CurrentUser, id uuid.UUID) error {
	if err := requireUser(user); err != nil {
		return err
	}
	var collection model.ContentCollection
	if err := s.db.First(&collection, "id = ?", id).Error; err != nil {
		return err
	}
	if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
		return err
	}
	if collection.IsDefault {
		return apperr.Conflict("studio.default_collection_protected", "default collection cannot be deleted")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("collection_id = ?", id).Delete(&model.ContentCollectionMembership{}).Error; err != nil {
			return err
		}
		return tx.Delete(&collection).Error
	})
}
