package feed

import (
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
)

type subscribedFeedSources struct {
	userIDs       []uuid.UUID
	channelIDs    []uuid.UUID
	collectionIDs []uuid.UUID
	feedSourceIDs []uuid.UUID
	visibleAfter  map[uuid.UUID]time.Time
}

func buildSubscribedFeedSources(subscriptions []model.Subscription, followedUserIDs []uuid.UUID, query FeedQuery) subscribedFeedSources {
	sources := subscribedFeedSources{
		userIDs:       append([]uuid.UUID(nil), followedUserIDs...),
		channelIDs:    make([]uuid.UUID, 0),
		collectionIDs: make([]uuid.UUID, 0),
		feedSourceIDs: make([]uuid.UUID, 0),
		visibleAfter:  make(map[uuid.UUID]time.Time),
	}
	for _, subscription := range subscriptions {
		if subscription.IsPaused || subscription.FeedSource == nil {
			continue
		}
		source := subscription.FeedSource
		switch source.SourceType {
		case "internal_user":
			if source.SourceID != nil {
				sources.userIDs = append(sources.userIDs, *source.SourceID)
			}
		case "internal_channel":
			if source.SourceID != nil {
				sources.channelIDs = append(sources.channelIDs, *source.SourceID)
			}
		case "internal_collection":
			if source.SourceID != nil {
				sources.collectionIDs = append(sources.collectionIDs, *source.SourceID)
			}
		case "external_rss":
			if query.ContentType != "blog" {
				sources.feedSourceIDs = append(sources.feedSourceIDs, source.ID)
				if subscription.ResumedAfter != nil {
					sources.visibleAfter[source.ID] = *subscription.ResumedAfter
				}
			}
		}
	}
	sources.userIDs = dedupeUUIDs(sources.userIDs)
	sources.channelIDs = dedupeUUIDs(sources.channelIDs)
	sources.collectionIDs = dedupeUUIDs(sources.collectionIDs)
	sources.feedSourceIDs = dedupeUUIDs(sources.feedSourceIDs)
	return sources
}
