package comment

import (
	"atoman/internal/model"

	"github.com/google/uuid"
)

type ForumCommentContext struct {
	Comment model.CommentEntry
	Target  model.DiscussionTarget
	Topic   model.ForumTopic
}

func (s *Service) ResolveForumComment(commentID uuid.UUID) (ForumCommentContext, error) {
	if commentID == uuid.Nil {
		return ForumCommentContext{}, ErrInvalidCommentID
	}
	entry, err := s.repo.findComment(s.db, commentID)
	if err != nil {
		return ForumCommentContext{}, ErrCommentNotFound
	}
	target, err := s.repo.findTargetByID(s.db, entry.TargetID)
	if err != nil || target.Kind != TargetKindForumTopic {
		return ForumCommentContext{}, ErrCommentNotFound
	}
	var topic model.ForumTopic
	if err := s.db.First(&topic, "id = ?", target.ResourceID).Error; err != nil {
		return ForumCommentContext{}, ErrCommentNotFound
	}
	return ForumCommentContext{Comment: entry, Target: target, Topic: topic}, nil
}
