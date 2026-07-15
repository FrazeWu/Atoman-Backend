package forum_engagement

import (
	"errors"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ToggleState struct {
	Liked      bool `json:"liked,omitempty"`
	Bookmarked bool `json:"bookmarked,omitempty"`
}

type Service struct {
	db       *gorm.DB
	comments *comment.Service
}

func NewService(db *gorm.DB, services ...*comment.Service) *Service {
	commentService := comment.NewService(db, comment.NewTargetRegistry(db))
	if len(services) > 0 && services[0] != nil {
		commentService = services[0]
	}
	return &Service{db: db, comments: commentService}
}

func (s *Service) ToggleTopicLike(user authctx.CurrentUser, topicID uuid.UUID) (ToggleState, error) {
	if user.ID == uuid.Nil {
		return ToggleState{}, apperr.Unauthorized("Login required")
	}
	if topicID == uuid.Nil {
		return ToggleState{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	if err := s.ensureTopicExists(topicID); err != nil {
		return ToggleState{}, err
	}

	state := ToggleState{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var like model.ForumLike
		result := tx.Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, "topic", topicID).Limit(1).Find(&like)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if err := tx.Delete(&like).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.ForumTopic{}).Where("id = ?", topicID).UpdateColumn("like_count", gorm.Expr("like_count - 1")).Error; err != nil {
				return err
			}
			state.Liked = false
			return nil
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
	return state, nil
}

func (s *Service) ToggleTopicBookmark(user authctx.CurrentUser, topicID uuid.UUID) (ToggleState, error) {
	if user.ID == uuid.Nil {
		return ToggleState{}, apperr.Unauthorized("Login required")
	}
	if topicID == uuid.Nil {
		return ToggleState{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	if err := s.ensureTopicExists(topicID); err != nil {
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
			if err := tx.Delete(&bookmark).Error; err != nil {
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

func (s *Service) ToggleReplyLike(user authctx.CurrentUser, replyID uuid.UUID) (ToggleState, error) {
	if user.ID == uuid.Nil {
		return ToggleState{}, apperr.Unauthorized("Login required")
	}
	if replyID == uuid.Nil {
		return ToggleState{}, apperr.BadRequest("validation.invalid_request", "reply_id is required")
	}
	if _, err := s.comments.ResolveForumComment(replyID); err != nil {
		return ToggleState{}, err
	}
	var like model.CommentLike
	result := s.db.Where("user_id = ? AND comment_id = ?", user.ID, replyID).Limit(1).Find(&like)
	if result.Error != nil {
		return ToggleState{}, result.Error
	}
	if result.RowsAffected > 0 {
		if err := s.comments.Unlike(user, replyID); err != nil {
			return ToggleState{}, err
		}
		return ToggleState{Liked: false}, nil
	}
	if err := s.comments.Like(user, replyID); err != nil {
		return ToggleState{}, err
	}
	return ToggleState{Liked: true}, nil
}

func (s *Service) ensureTopicExists(topicID uuid.UUID) error {
	var topic model.ForumTopic
	if err := s.db.Select("id").First(&topic, "id = ?", topicID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("forum.topic_not_found", "Forum topic not found")
		}
		return err
	}
	return nil
}
