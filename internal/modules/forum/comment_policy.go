package forum

import (
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/modules/forum_moderation"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var _ comment.ForumPolicy = (*Service)(nil)

func (s *Service) CanViewTopic(viewer comment.Viewer, topicID uuid.UUID) error {
	user := authctx.CurrentUser{Role: viewer.Role}
	if viewer.UserID != nil {
		user.ID = *viewer.UserID
	}
	if user.Role == "" {
		user.Role = authctx.RoleAnonymous
	}
	if _, err := s.repo.GetTopic(user, topicID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("forum.topic_not_found", "Forum topic not found")
		}
		return err
	}
	return nil
}

func (s *Service) CheckCreateComment(tx *gorm.DB, user authctx.CurrentUser, topicID uuid.UUID, content string) error {
	var topic model.ForumTopic
	if err := tx.Select("id", "category_id").First(&topic, "id = ?", topicID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("forum.topic_not_found", "Forum topic not found")
		}
		return err
	}
	policy := &Service{db: tx, repo: NewRepo(tx), trust: s.trust.WithDB(tx)}
	if err := policy.CanComment(user, topic.CategoryID); err != nil {
		return err
	}
	silenced, err := forum_moderation.IsUserSilenced(tx, user.ID, time.Now().UTC())
	if err != nil {
		return err
	}
	if silenced {
		return apperr.Forbidden("forum.user_silenced", "You are temporarily silenced")
	}
	return policy.trust.CheckCreateReply(user, content)
}

func (s *Service) CheckUpdateComment(tx *gorm.DB, authorID, commentID uuid.UUID, content string) error {
	return s.trust.WithDB(tx).CheckUpdateReply(authorID, commentID, content)
}

func (s *Service) CommentNotificationAudience(tx *gorm.DB, topicID, actorID uuid.UUID) (string, []uuid.UUID, error) {
	var topic model.ForumTopic
	if err := tx.Select("id", "user_id", "category_id", "title").First(&topic, "id = ?", topicID).Error; err != nil {
		return "", nil, err
	}
	followerIDs, err := NewRepo(tx).ListFollowerIDs(model.ForumFollowTargetTopic, topicID.String())
	if err != nil {
		return "", nil, err
	}
	candidates := make([]uuid.UUID, 0, len(followerIDs)+1)
	seen := make(map[uuid.UUID]struct{}, len(followerIDs)+1)
	for _, userID := range append([]uuid.UUID{topic.UserID}, followerIDs...) {
		if userID == actorID {
			continue
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		candidates = append(candidates, userID)
	}
	allowed, err := NewRepo(tx).FilterUsersWhoCanViewCategory(candidates, topic.CategoryID)
	return topic.Title, allowed, err
}

func (s *Service) EvaluateTrust(userID uuid.UUID) {
	s.evaluateTrustAfterWrite(userID)
}
