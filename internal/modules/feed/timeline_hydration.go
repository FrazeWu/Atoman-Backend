package feed

import (
	"fmt"

	"atoman/internal/model"
	"github.com/google/uuid"
)

// hydrateTimelineFeedItems reads the wide FeedItem columns only for the final
// response page. The duplicate clustering metadata was computed from the
// lightweight scan and must survive replacement by the database model.
func (s *Service) hydrateTimelineFeedItems(page []TimelineItemDTO) error {
	ids := make([]uuid.UUID, 0, len(page))
	for _, item := range page {
		if item.FeedItem != nil {
			ids = append(ids, item.FeedItem.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	loaded, err := s.repo.LoadFeedItemsForTimelinePage(ids)
	if err != nil {
		return err
	}
	for i := range page {
		selected := page[i].FeedItem
		if selected == nil {
			continue
		}
		full, ok := loaded[selected.ID]
		if !ok {
			return fmt.Errorf("feed item %s disappeared during timeline hydration", selected.ID)
		}
		transferDuplicateMetadata(&full, selected)
		page[i].FeedItem = &full
	}
	return nil
}

func transferDuplicateMetadata(dst *model.FeedItem, src *model.FeedItem) {
	dst.IsDuplicate = src.IsDuplicate
	dst.DuplicateCount = src.DuplicateCount
	dst.DuplicateOfID = src.DuplicateOfID
	dst.DuplicateItemIDs = src.DuplicateItemIDs
	dst.DuplicateSources = src.DuplicateSources
}
