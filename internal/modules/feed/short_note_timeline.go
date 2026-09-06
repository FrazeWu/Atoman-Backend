package feed

import (
	"atoman/internal/model"

	"github.com/google/uuid"
)

func (s *Service) listSubscribedShortNotes(userID uuid.UUID, userIDs []uuid.UUID) ([]model.ShortNote, map[uuid.UUID]bool, error) {
	notes := make([]model.ShortNote, 0)
	read := make(map[uuid.UUID]bool)
	if len(userIDs) == 0 || !s.db.Migrator().HasTable(&model.ShortNote{}) {
		return notes, read, nil
	}
	var err error
	notes, err = s.repo.ListPublishedShortNotesByUserIDs(dedupeUUIDs(userIDs))
	if err != nil {
		return nil, nil, err
	}
	if len(notes) == 0 || !s.db.Migrator().HasTable(&model.ShortNoteRead{}) {
		return notes, read, nil
	}
	ids := make([]uuid.UUID, 0, len(notes))
	for _, note := range notes {
		ids = append(ids, note.ID)
	}
	var rows []model.ShortNoteRead
	if err := s.db.Where("user_id = ? AND short_note_id IN ?", userID, ids).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		read[row.ShortNoteID] = true
	}
	return notes, read, nil
}

func (s *Service) subscribedShortNoteAuthorIDs(userID uuid.UUID) ([]uuid.UUID, error) {
	authorIDs, err := s.repo.ListFollowedUserIDs(userID)
	if err != nil {
		return nil, err
	}
	subscriptions, err := s.repo.ListSubscriptionsWithSources(userID, FeedQuery{})
	if err != nil {
		return nil, err
	}
	for _, subscription := range subscriptions {
		if subscription.FeedSource == nil || subscription.FeedSource.SourceType != "internal_user" || subscription.FeedSource.SourceID == nil {
			continue
		}
		authorIDs = append(authorIDs, *subscription.FeedSource.SourceID)
	}
	return dedupeUUIDs(authorIDs), nil
}

func (s *Service) subscriptionShortNoteIDs(userID, subscriptionID uuid.UUID) ([]uuid.UUID, error) {
	var subscription model.Subscription
	if err := s.db.Preload("FeedSource").Where("id = ? AND user_id = ?", subscriptionID, userID).First(&subscription).Error; err != nil {
		return nil, err
	}
	if subscription.FeedSource == nil || subscription.FeedSource.SourceType != "internal_user" || subscription.FeedSource.SourceID == nil {
		return []uuid.UUID{}, nil
	}
	notes, err := s.repo.ListPublishedShortNotesByUserIDs([]uuid.UUID{*subscription.FeedSource.SourceID})
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(notes))
	for _, note := range notes {
		ids = append(ids, note.ID)
	}
	return ids, nil
}
