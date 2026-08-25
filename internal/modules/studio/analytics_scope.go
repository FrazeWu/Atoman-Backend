package studio

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) filterAnalyticsScope(
	userID, channelID uuid.UUID,
	module Module,
	query AnalyticsQuery,
	titles map[uuid.UUID]string,
) (map[uuid.UUID]string, map[uuid.UUID]map[string]int64, []uuid.UUID, bool, error) {
	restricted := query.CollectionID != uuid.Nil || query.ContentID != uuid.Nil
	allowed := make(map[uuid.UUID]struct{}, len(titles))
	for id := range titles {
		allowed[id] = struct{}{}
	}
	if query.CollectionID != uuid.Nil {
		collectionID, err := s.resolveUnifiedCollectionID(channelID, query.CollectionID)
		if err != nil {
			return nil, nil, nil, false, err
		}
		var rows []struct {
			ID uuid.UUID `gorm:"column:id"`
		}
		if err := s.db.Table("content_collection_memberships AS memberships").
			Joins("JOIN content_entries AS posts ON posts.id = memberships.content_id").
			Joins("LEFT JOIN content_episode_extensions AS episodes ON episodes.content_id = posts.id").
			Joins("LEFT JOIN content_video_extensions AS videos ON videos.content_id = posts.id").
			Select("COALESCE(episodes.episode_id, videos.video_id, posts.id) AS id").
			Where("memberships.collection_id = ? AND posts.channel_id = ? AND posts.author_id = ? AND posts.kind = ? AND posts.deleted_at IS NULL", collectionID, channelID, userID, string(module)).
			Scan(&rows).Error; err != nil {
			return nil, nil, nil, false, err
		}
		collectionIDs := make(map[uuid.UUID]struct{}, len(rows))
		for _, row := range rows {
			collectionIDs[row.ID] = struct{}{}
		}
		for id := range allowed {
			if _, exists := collectionIDs[id]; !exists {
				delete(allowed, id)
			}
		}
	}
	if query.ContentID != uuid.Nil {
		if _, exists := allowed[query.ContentID]; !exists {
			allowed = map[uuid.UUID]struct{}{}
		} else {
			allowed = map[uuid.UUID]struct{}{query.ContentID: {}}
		}
	}
	filteredTitles := make(map[uuid.UUID]string, len(allowed))
	filteredMetrics := make(map[uuid.UUID]map[string]int64, len(allowed))
	contentIDs := make([]uuid.UUID, 0, len(allowed))
	for id := range allowed {
		filteredTitles[id] = titles[id]
		filteredMetrics[id] = emptyMetricMap(metricNamesByModule[module])
		contentIDs = append(contentIDs, id)
	}
	return filteredTitles, filteredMetrics, contentIDs, restricted, nil
}

func (s *Service) analyticsEventsQuery(channelID uuid.UUID, module Module, from, to time.Time, contentIDs []uuid.UUID, restricted bool) *gorm.DB {
	query := s.db.Where("channel_id = ? AND content_type = ? AND created_at >= ? AND created_at < ?", channelID, module, from, to)
	if restricted {
		if len(contentIDs) == 0 {
			return query.Where("1 = 0")
		}
		return query.Where("content_id IN ?", contentIDs)
	}
	return query
}
