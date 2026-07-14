package comment

import (
	"fmt"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	NotificationTypeReply   = "comment_reply"
	NotificationTypeMention = "comment_mention"
	NotificationTypeMarked  = "comment_marked"
	NotificationTypeLike    = "comment_like"
)

func notificationMeta(entry model.CommentEntry, target ResolvedTarget) model.NotificationMeta {
	rootID := entry.ID
	if entry.RootID != nil {
		rootID = *entry.RootID
	}
	return model.NotificationMeta{
		"target_kind": target.Kind,
		"resource_id": target.ResourceID.String(),
		"comment_id":  entry.ID.String(),
		"root_id":     rootID.String(),
	}
}

func (s *Service) notifyCreatedComment(tx *gorm.DB, entry model.CommentEntry, target ResolvedTarget, replyAuthorID *uuid.UUID, mentions []MentionInput) error {
	recipients := make([]uuid.UUID, 0, len(mentions)+1)
	types := make(map[uuid.UUID]string, len(mentions)+1)
	if replyAuthorID != nil && *replyAuthorID != entry.AuthorID {
		recipients = append(recipients, *replyAuthorID)
		types[*replyAuthorID] = NotificationTypeReply
	}
	for _, mention := range mentions {
		if mention.UserID == entry.AuthorID {
			continue
		}
		if _, exists := types[mention.UserID]; exists {
			continue
		}
		recipients = append(recipients, mention.UserID)
		types[mention.UserID] = NotificationTypeMention
	}
	for _, recipientID := range recipients {
		if err := createImmediateNotification(tx, recipientID, entry.AuthorID, types[recipientID], entry.ID, notificationMeta(entry, target)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) notifyNewEditMentions(tx *gorm.DB, entry model.CommentEntry, target ResolvedTarget, actorID uuid.UUID, mentions []MentionInput) error {
	seen := map[uuid.UUID]struct{}{actorID: {}}
	for _, mention := range mentions {
		if _, exists := seen[mention.UserID]; exists {
			continue
		}
		seen[mention.UserID] = struct{}{}
		var count int64
		if err := tx.Model(&model.Notification{}).Where("recipient_id = ? AND source_type = ? AND source_id = ?", mention.UserID, "comment_event", entry.ID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if err := createImmediateNotification(tx, mention.UserID, actorID, NotificationTypeMention, entry.ID, notificationMeta(entry, target)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) notifyMarkedComment(tx *gorm.DB, entry model.CommentEntry, target ResolvedTarget, actorID uuid.UUID) error {
	if entry.AuthorID == actorID {
		return nil
	}
	return createImmediateNotification(tx, entry.AuthorID, actorID, NotificationTypeMarked, entry.ID, notificationMeta(entry, target))
}

func createImmediateNotification(tx *gorm.DB, recipientID, actorID uuid.UUID, notificationType string, sourceID uuid.UUID, meta model.NotificationMeta) error {
	notification := model.Notification{RecipientID: recipientID, ActorID: &actorID, Type: notificationType, SourceType: "comment_event", SourceID: sourceID, Meta: meta}
	result := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "recipient_id"}, {Name: "source_type"}, {Name: "source_id"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "aggregation_key = '' AND deleted_at IS NULL"},
		}},
		DoNothing: true,
	}).Create(&notification)
	if result.Error == nil {
		return nil
	}
	var count int64
	if err := tx.Model(&model.Notification{}).Where("recipient_id = ? AND source_type = ? AND source_id = ? AND aggregation_key = ''", recipientID, "comment_event", sourceID).Count(&count).Error; err == nil && count > 0 {
		return nil
	}
	return fmt.Errorf("create comment notification: %w", result.Error)
}

func (s *Service) syncLikeNotification(tx *gorm.DB, entry model.CommentEntry, actorID uuid.UUID, likeCount int64, liked bool) error {
	if entry.AuthorID == actorID {
		return nil
	}
	key := "comment_like:" + entry.ID.String()
	query := tx.Where("recipient_id = ? AND aggregation_key = ? AND read_at IS NULL", entry.AuthorID, key)
	if likeCount == 0 {
		return query.Unscoped().Delete(&model.Notification{}).Error
	}
	meta := model.NotificationMeta{}
	var target model.DiscussionTarget
	if err := tx.First(&target, "id = ?", entry.TargetID).Error; err != nil {
		return err
	}
	resolved := ResolvedTarget{Kind: target.Kind, ResourceID: target.ResourceID}
	meta = notificationMeta(entry, resolved)
	meta["like_count"] = likeCount
	meta["actor_count"] = likeCount
	var existing model.Notification
	if err := query.First(&existing).Error; err == nil {
		return tx.Model(&existing).Updates(map[string]any{"actor_id": actorID, "meta": meta}).Error
	} else if !isNotFound(err) {
		return err
	}
	if !liked {
		return nil
	}
	notification := model.Notification{RecipientID: entry.AuthorID, ActorID: &actorID, Type: NotificationTypeLike, SourceType: "comment_like", SourceID: entry.ID, AggregationKey: key, Meta: meta}
	return tx.Create(&notification).Error
}
