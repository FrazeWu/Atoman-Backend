package forum_engagement

import (
	"errors"
	"log"

	"atoman/internal/model"
	"atoman/internal/modules/forum"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	coreservice "atoman/internal/service"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ToggleState struct {
	Liked      bool `json:"liked,omitempty"`
	Bookmarked bool `json:"bookmarked,omitempty"`
}

type Service struct {
	db    *gorm.DB
	trust *coreservice.ForumTrustService
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db, trust: coreservice.NewForumTrustService(db)}
}

func (s *Service) ToggleTopicLike(user authctx.CurrentUser, topicID uuid.UUID) (ToggleState, error) {
	if user.ID == uuid.Nil {
		return ToggleState{}, apperr.Unauthorized("Login required")
	}
	if topicID == uuid.Nil {
		return ToggleState{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	topic, err := s.visibleTopic(user, topicID)
	if err != nil {
		return ToggleState{}, err
	}
	authorID := topic.UserID

	state := ToggleState{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&model.ForumTopic{}, "id = ?", topicID).Error; err != nil {
			return err
		}
		var like model.ForumLike
		result := tx.Unscoped().Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, "topic", topicID).Limit(1).Find(&like)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 && !like.DeletedAt.Valid {
			if err := tx.Unscoped().Delete(&like).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.ForumTopic{}).Where("id = ?", topicID).UpdateColumn("like_count", gorm.Expr("CASE WHEN like_count > 0 THEN like_count - 1 ELSE 0 END")).Error; err != nil {
				return err
			}
			state.Liked = false
			return nil
		}
		if result.RowsAffected > 0 {
			if err := tx.Unscoped().Delete(&like).Error; err != nil {
				return err
			}
		}

		like = model.ForumLike{UserID: user.ID, TargetType: "topic", TargetID: topicID}
		if err := tx.Create(&like).Error; err != nil {
			var existing model.ForumLike
			lookup := tx.Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, "topic", topicID).Limit(1).Find(&existing)
			if lookup.Error == nil && lookup.RowsAffected > 0 {
				state.Liked = true
				return nil
			}
			return err
		}
		if err := tx.Model(&model.ForumTopic{}).Where("id = ?", topicID).UpdateColumn("like_count", gorm.Expr("like_count + 1")).Error; err != nil {
			return err
		}
		state.Liked = true
		return nil
	})
	if err != nil {
		return ToggleState{}, err
	}
	s.evaluateAuthorAfterLike(state.Liked, authorID)
	return state, nil
}

func (s *Service) ToggleTopicBookmark(user authctx.CurrentUser, topicID uuid.UUID) (ToggleState, error) {
	if user.ID == uuid.Nil {
		return ToggleState{}, apperr.Unauthorized("Login required")
	}
	if topicID == uuid.Nil {
		return ToggleState{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	if _, err := s.visibleTopic(user, topicID); err != nil {
		return ToggleState{}, err
	}

	state := ToggleState{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var bookmark model.ForumBookmark
		result := tx.Where("user_id = ? AND topic_id = ?", user.ID, topicID).Limit(1).Find(&bookmark)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if err := tx.Unscoped().Delete(&bookmark).Error; err != nil {
				return err
			}
			state.Bookmarked = false
			return nil
		}

		bookmark = model.ForumBookmark{UserID: user.ID, TopicID: topicID}
		if err := tx.Create(&bookmark).Error; err != nil {
			var existing model.ForumBookmark
			lookup := tx.Where("user_id = ? AND topic_id = ?", user.ID, topicID).Limit(1).Find(&existing)
			if lookup.Error == nil && lookup.RowsAffected > 0 {
				state.Bookmarked = true
				return nil
			}
			return err
		}
		state.Bookmarked = true
		return nil
	})
	if err != nil {
		return ToggleState{}, err
	}
	return state, nil
}

func (s *Service) evaluateAuthorAfterLike(liked bool, authorID uuid.UUID) {
	if !liked {
		return
	}
	if _, err := s.trust.Evaluate(authorID); err != nil {
		log.Printf("forum trust evaluation failed for user %s after like: %v", authorID, err)
	}
}

func (s *Service) visibleTopic(user authctx.CurrentUser, topicID uuid.UUID) (model.ForumTopic, error) {
	var topic model.ForumTopic
	if err := s.db.Select("id", "user_id", "category_id").First(&topic, "id = ?", topicID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ForumTopic{}, apperr.NotFound("forum.topic_not_found", "Forum topic not found")
		}
		return model.ForumTopic{}, err
	}
	if err := forum.NewService(s.db).CanViewCategory(user, topic.CategoryID); err != nil {
		return model.ForumTopic{}, err
	}
	return topic, nil
}
