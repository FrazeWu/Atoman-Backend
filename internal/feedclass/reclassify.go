package feedclass

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

type Change struct {
	SourceID     string
	Title        string
	RSSURL       string
	Current      string
	Next         string
	RecentLinks  []string
}

func CollectChanges(db *gorm.DB) ([]Change, error) {
	var sources []model.FeedSource
	if err := db.
		Where("deleted_at IS NULL").
		Where("hidden = ?", false).
		Where("source_type = ?", "external_rss").
		Find(&sources).Error; err != nil {
		return nil, err
	}

	changes := make([]Change, 0)
	for _, source := range sources {
		var items []model.FeedItem
		if err := db.
			Where("feed_source_id = ?", source.ID).
			Order("published_at DESC NULLS LAST").
			Order("created_at DESC").
			Limit(3).
			Find(&items).Error; err != nil {
			return nil, err
		}

		recentItems := make([]RecentItem, 0, len(items))
		recentLinks := make([]string, 0, len(items))
		for _, item := range items {
			recentItems = append(recentItems, RecentItem{
				Title:         item.Title,
				Link:          item.Link,
				EnclosureType: item.EnclosureType,
			})
			if item.Link != "" {
				recentLinks = append(recentLinks, item.Link)
			}
		}

		nextCategory := Classify(Source{
			Title:       source.Title,
			RSSURL:      source.RssURL,
			RecentItems: recentItems,
		})
		if source.Category == nextCategory {
			continue
		}

		changes = append(changes, Change{
			SourceID:    source.ID.String(),
			Title:       source.Title,
			RSSURL:      source.RssURL,
			Current:     source.Category,
			Next:        nextCategory,
			RecentLinks: recentLinks,
		})
	}

	return changes, nil
}

func ApplyChanges(db *gorm.DB, changes []Change) error {
	for _, change := range changes {
		if err := db.Model(&model.FeedSource{}).Where("id = ?", change.SourceID).Update("category", change.Next).Error; err != nil {
			return err
		}
	}
	return nil
}
