package lifecycle

import (
	"errors"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const publicationNotificationBatchSize = 200

// publicationRecipientBatch pages over opted-in recipients, without loading
// every subscriber/follower or making a preference query for every user.
func (s *Service) publicationRecipientBatch(event model.ContentPublicationEvent, content contentSummary, after uuid.UUID, hasFollows bool) ([]uuid.UUID, error) {
	prefConditions := "(source_type = ? AND source_id = ?) OR (source_type = ? AND source_id = ?)"
	prefArgs := []any{"internal_channel", event.ChannelID, "internal_user", event.OwnerID}
	sourceConditions := "(fs.source_type = ? AND fs.source_id = ?) OR (fs.source_type = ? AND fs.source_id = ?)"
	sourceArgs := []any{"internal_channel", event.ChannelID, "internal_user", event.OwnerID}
	if len(content.CollectionIDs) != 0 {
		prefConditions += " OR (source_type = ? AND source_id IN ?)"
		prefArgs = append(prefArgs, "internal_collection", content.CollectionIDs)
		sourceConditions += " OR (fs.source_type = ? AND fs.source_id IN ?)"
		sourceArgs = append(sourceArgs, "internal_collection", content.CollectionIDs)
	}

	// A user must both subscribe/follow the publisher and explicitly opt into
	// one of this content's channel/user/collection notification scopes.
	eligible := "EXISTS (SELECT 1 FROM subscriptions AS sub JOIN feed_sources AS fs ON fs.id = sub.feed_source_id " +
		"WHERE sub.user_id = content_notification_preferences.user_id AND sub.deleted_at IS NULL AND fs.deleted_at IS NULL " +
		"AND (" + sourceConditions + "))"
	eligibleArgs := sourceArgs
	if hasFollows {
		eligible += " OR EXISTS (SELECT 1 FROM follows AS f WHERE f.follower_id = content_notification_preferences.user_id AND f.following_id = ?)"
		eligibleArgs = append(eligibleArgs, event.OwnerID)
	}

	var recipients []uuid.UUID
	err := s.db.Model(&model.ContentNotificationPreference{}).
		Distinct("user_id").
		Where("mode = ? AND user_id > ? AND user_id <> ?", "all", after, event.OwnerID).
		Where("("+prefConditions+")", prefArgs...).
		Where("("+eligible+")", eligibleArgs...).
		Order("user_id ASC").
		Limit(publicationNotificationBatchSize).
		Pluck("user_id", &recipients).Error
	return recipients, err
}

// renewPublicationLease is a lightweight fence before an external or long
// database operation. A lost lease must not write new notifications.
func (s *Service) renewPublicationLease(event model.ContentPublicationEvent) (bool, error) {
	if event.ID == uuid.Nil {
		return true, nil // Direct dispatch calls without a queue event.
	}
	renewed := s.db.Model(&model.ContentPublicationEvent{}).
		Where("id = ? AND status = ? AND lease_version = ?", event.ID, "processing", event.LeaseVersion).
		Update("updated_at", time.Now().UTC())
	if renewed.Error != nil {
		return false, renewed.Error
	}
	return renewed.RowsAffected == 1, nil
}

// writePublicationNotificationBatch locks the event before inserting a bounded
// batch, protecting delivery against concurrent stale-lease reclamation.
// Existing unique notification indexes make a retry idempotent.
func (s *Service) writePublicationNotificationBatch(event model.ContentPublicationEvent, content contentSummary, recipients []uuid.UUID) (bool, error) {
	if len(recipients) == 0 {
		return true, nil
	}
	actorID := event.OwnerID
	notifications := make([]model.Notification, 0, len(recipients))
	for _, recipientID := range recipients {
		notifications = append(notifications, model.Notification{
			RecipientID: recipientID, ActorID: &actorID, Type: "content_published",
			SourceType: "content_publication", SourceID: event.ContentID,
			Meta: model.NotificationMeta{"module": event.ContentType, "title": content.Title, "path": content.Path, "channel_id": event.ChannelID.String()},
		})
	}
	write := func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&notifications, 100).Error
	}
	if event.ID == uuid.Nil {
		return true, write(s.db)
	}
	owned := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var lease model.ContentPublicationEvent
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND lease_version = ?", event.ID, "processing", event.LeaseVersion).
			First(&lease).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		owned = true
		if err := tx.Model(&model.ContentPublicationEvent{}).Where("id = ?", event.ID).
			Update("updated_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		return write(tx)
	})
	return owned && err == nil, err
}
