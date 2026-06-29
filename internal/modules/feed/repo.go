package feed

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

type ExploreSourceRow struct {
	ID                uuid.UUID                 `json:"id"`
	Title             string                    `json:"title"`
	RSSURL            string                    `json:"rss_url"`
	SubscriptionCount int64                     `json:"subscription_count"`
	RecentItemCount   int64                     `json:"recent_item_count"`
	LastPublishedAt   *time.Time                `json:"last_published_at"`
	RecentItems       []ExploreSourceRecentItem `json:"recent_items"`
}

type ExploreSourceRecentItem struct {
	ID          uuid.UUID `json:"id"`
	Title       string    `json:"title"`
	PublishedAt time.Time `json:"published_at"`
}

func (r *Repo) ListSubscriptionsWithSources(userID uuid.UUID, query FeedQuery) ([]model.Subscription, error) {
	db := r.db.Model(&model.Subscription{}).
		Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
		Where("subscriptions.user_id = ? AND feed_sources.hidden = ?", userID, false)
	if query.SourceType != "" {
		db = db.Where("feed_sources.source_type = ?", query.SourceType)
	}
	if query.SourceID != uuid.Nil {
		db = db.Where("subscriptions.id = ?", query.SourceID)
	}
	if query.GroupID != uuid.Nil {
		db = db.Where("subscriptions.subscription_group_id = ?", query.GroupID)
	}
	var subscriptions []model.Subscription
	err := db.Preload("FeedSource").Find(&subscriptions).Error
	return subscriptions, err
}

func (r *Repo) ListVisibleFeedSources(query FeedQuery) ([]model.FeedSource, error) {
	db := r.db.Model(&model.FeedSource{}).Where("hidden = ?", false)
	if query.SourceType != "" {
		db = db.Where("source_type = ?", query.SourceType)
	}
	if query.SourceID != uuid.Nil {
		db = db.Where("id = ?", query.SourceID)
	}
	var sources []model.FeedSource
	err := db.Find(&sources).Error
	return sources, err
}

func (r *Repo) ListPublishedPostsByUserIDs(userIDs []uuid.UUID) ([]model.Post, error) {
	if len(userIDs) == 0 {
		return []model.Post{}, nil
	}
	var posts []model.Post
	err := r.db.Preload("User").Preload("Channel").Preload("Collections").
		Where("status = ?", "published").
		Where("user_id IN ?", userIDs).
		Find(&posts).Error
	return posts, err
}

func (r *Repo) ListPublishedPostsByChannelIDs(channelIDs []uuid.UUID) ([]model.Post, error) {
	if len(channelIDs) == 0 {
		return []model.Post{}, nil
	}
	var posts []model.Post
	err := r.db.Preload("User").Preload("Channel").Preload("Collections").
		Where("status = ?", "published").
		Where("channel_id IN ?", channelIDs).
		Find(&posts).Error
	return posts, err
}

func (r *Repo) ListPublishedPostsByCollectionIDs(collectionIDs []uuid.UUID) ([]model.Post, error) {
	if len(collectionIDs) == 0 {
		return []model.Post{}, nil
	}
	var posts []model.Post
	err := r.db.Preload("User").Preload("Channel").Preload("Collections").
		Joins("JOIN post_collections ON post_collections.post_id = posts.id").
		Where("posts.status = ?", "published").
		Where("post_collections.collection_id IN ?", collectionIDs).
		Find(&posts).Error
	return posts, err
}

func (r *Repo) ListFeedItemsBySourceIDs(feedSourceIDs []uuid.UUID) ([]model.FeedItem, error) {
	if len(feedSourceIDs) == 0 {
		return []model.FeedItem{}, nil
	}
	var items []model.FeedItem
	err := r.db.Preload("FeedSource").
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.feed_source_id IN ? AND feed_sources.hidden = ?", feedSourceIDs, false).
		Order("feed_items.published_at DESC").
		Find(&items).Error
	return items, err
}

func (r *Repo) ListFeedItemsBySourceIDsPaged(feedSourceIDs []uuid.UUID, limit int, offset int) ([]model.FeedItem, error) {
	if len(feedSourceIDs) == 0 {
		return []model.FeedItem{}, nil
	}
	var items []model.FeedItem
	err := r.db.Preload("FeedSource").
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.feed_source_id IN ? AND feed_sources.hidden = ?", feedSourceIDs, false).
		Order("feed_items.published_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&items).Error
	return items, err
}

func (r *Repo) ListFeedItemsBySourceID(feedSourceID uuid.UUID, limit int, offset int) ([]model.FeedItem, error) {
	var items []model.FeedItem
	err := r.db.Preload("FeedSource").
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.feed_source_id = ?", feedSourceID).
		Where("feed_sources.hidden = ?", false).
		Order("feed_items.published_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&items).Error
	return items, err
}

func (r *Repo) CountFeedItemsBySourceID(feedSourceID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.Model(&model.FeedItem{}).
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.feed_source_id = ?", feedSourceID).
		Where("feed_sources.hidden = ?", false).
		Count(&count).Error
	return count, err
}

func (r *Repo) CountFeedItemsBySourceIDs(feedSourceIDs []uuid.UUID) (int64, error) {
	if len(feedSourceIDs) == 0 {
		return 0, nil
	}
	var count int64
	err := r.db.Model(&model.FeedItem{}).
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.feed_source_id IN ? AND feed_sources.hidden = ?", feedSourceIDs, false).
		Count(&count).Error
	return count, err
}

func (r *Repo) ListReadItems(userID uuid.UUID, feedItemIDs []uuid.UUID) ([]model.FeedItemRead, error) {
	if len(feedItemIDs) == 0 {
		return []model.FeedItemRead{}, nil
	}
	var reads []model.FeedItemRead
	err := r.db.Where("user_id = ? AND feed_item_id IN ?", userID, feedItemIDs).Find(&reads).Error
	return reads, err
}

func (r *Repo) MarkRead(userID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	now := r.db.NowFunc()
	for _, id := range ids {
		read := model.FeedItemRead{UserID: userID, FeedItemID: id, ReadAt: now}
		if err := r.db.Where("user_id = ? AND feed_item_id = ?", userID, id).FirstOrCreate(&read).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) ListSubscribedExternalFeedItems(userID uuid.UUID) ([]model.FeedItem, error) {
	var items []model.FeedItem
	err := r.db.
		Joins("JOIN subscriptions ON subscriptions.feed_source_id = feed_items.feed_source_id").
		Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
		Where("subscriptions.user_id = ? AND feed_sources.source_type = ? AND feed_sources.hidden = ?", userID, "external_rss", false).
		Preload("FeedSource").
		Find(&items).Error
	return items, err
}

func (r *Repo) DeleteReads(userID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.Where("user_id = ? AND feed_item_id IN ?", userID, ids).Delete(&model.FeedItemRead{}).Error
}

func (r *Repo) ListExplorePosts(limit int, offset int) ([]model.Post, error) {
	var posts []model.Post
	err := r.db.Preload("User").Preload("Channel").Preload("Collections").
		Where("status = ?", "published").
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&posts).Error
	return posts, err
}

func (r *Repo) CountExplorePosts() (int64, error) {
	var count int64
	err := r.db.Model(&model.Post{}).Where("status = ?", "published").Count(&count).Error
	return count, err
}

func (r *Repo) ListExploreFeedItems(sort string, limit int, offset int) ([]model.FeedItem, error) {
	var items []model.FeedItem
	db := r.db.Preload("FeedSource").
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_sources.hidden = ?", false).
		Offset(offset).
		Limit(limit)
	if sort == "popular" {
		db = db.Select("feed_items.*, (SELECT COUNT(*) FROM feed_item_stars WHERE feed_item_stars.feed_item_id = feed_items.id) as star_count").Order("star_count DESC, published_at DESC")
	} else {
		db = db.Order("RANDOM()")
	}
	err := db.Find(&items).Error
	return items, err
}

func (r *Repo) CountExploreFeedItems() (int64, error) {
	var count int64
	err := r.db.Model(&model.FeedItem{}).
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_sources.hidden = ?", false).
		Count(&count).Error
	return count, err
}

func (r *Repo) ListExploreSources(limit int, offset int) ([]ExploreSourceRow, error) {
	type exploreSourceRowRaw struct {
		ID                uuid.UUID
		Title             string
		RSSURL            string
		SubscriptionCount int64
		RecentItemCount   int64
		LastPublishedAt   sql.NullString
	}

	var rawRows []exploreSourceRowRaw
	err := r.db.Table("feed_sources").
		Select(`
			feed_sources.id,
			feed_sources.title,
			feed_sources.rss_url,
			COUNT(DISTINCT subscriptions.id) AS subscription_count,
			COUNT(DISTINCT feed_items.id) AS recent_item_count,
			MAX(feed_items.published_at) AS last_published_at
		`).
		Joins("LEFT JOIN subscriptions ON subscriptions.feed_source_id = feed_sources.id").
		Joins("LEFT JOIN feed_items ON feed_items.feed_source_id = feed_sources.id").
		Where("feed_sources.source_type = ?", "external_rss").
		Where("feed_sources.hidden = ?", false).
		Group("feed_sources.id").
		Having("COUNT(DISTINCT feed_items.id) > 0").
		Order("subscription_count DESC").
		Order("last_published_at DESC NULLS LAST").
		Order("feed_sources.created_at DESC").
		Offset(offset).
		Limit(limit).
		Scan(&rawRows).Error
	if err != nil {
		return nil, err
	}

	rows := make([]ExploreSourceRow, 0, len(rawRows))
	sourceIDs := make([]uuid.UUID, 0, len(rawRows))
	for _, raw := range rawRows {
		row := ExploreSourceRow{
			ID:                raw.ID,
			Title:             raw.Title,
			RSSURL:            raw.RSSURL,
			SubscriptionCount: raw.SubscriptionCount,
			RecentItemCount:   raw.RecentItemCount,
		}
		if raw.LastPublishedAt.Valid {
			parsed, parseErr := parseExploreSourceTimestamp(raw.LastPublishedAt.String)
			if parseErr != nil {
				return nil, parseErr
			}
			row.LastPublishedAt = &parsed
		}
		rows = append(rows, row)
		sourceIDs = append(sourceIDs, raw.ID)
	}

	if err := r.attachExploreSourceRecentItems(rows, sourceIDs); err != nil {
		return nil, err
	}

	return rows, nil
}

func (r *Repo) attachExploreSourceRecentItems(rows []ExploreSourceRow, sourceIDs []uuid.UUID) error {
	if len(rows) == 0 {
		return nil
	}

	var items []model.FeedItem
	if err := r.db.
		Where("feed_source_id IN ?", sourceIDs).
		Order("published_at DESC").
		Order("created_at DESC").
		Find(&items).Error; err != nil {
		return err
	}

	rowIndexBySourceID := make(map[uuid.UUID]int, len(rows))
	for i, row := range rows {
		rowIndexBySourceID[row.ID] = i
	}

	countBySourceID := make(map[uuid.UUID]int, len(rows))
	for _, item := range items {
		rowIndex, ok := rowIndexBySourceID[item.FeedSourceID]
		if !ok || countBySourceID[item.FeedSourceID] >= 3 {
			continue
		}
		rows[rowIndex].RecentItems = append(rows[rowIndex].RecentItems, ExploreSourceRecentItem{
			ID:          item.ID,
			Title:       item.Title,
			PublishedAt: item.PublishedAt,
		})
		countBySourceID[item.FeedSourceID]++
	}

	return nil
}

func (r *Repo) CountExploreSources() (int64, error) {
	var count int64
	err := r.db.Table("feed_sources").
		Joins("JOIN feed_items ON feed_items.feed_source_id = feed_sources.id").
		Where("feed_sources.source_type = ?", "external_rss").
		Where("feed_sources.hidden = ?", false).
		Distinct("feed_sources.id").
		Count(&count).Error
	return count, err
}

func parseExploreSourceTimestamp(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05Z07:00",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("parse explore source timestamp %q", raw)
}

func (r *Repo) FeedItemExists(id uuid.UUID) (bool, error) {
	var count int64
	err := r.db.Model(&model.FeedItem{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *Repo) FindStar(userID uuid.UUID, feedItemID uuid.UUID) (model.FeedItemStar, error) {
	var star model.FeedItemStar
	err := r.db.Where("user_id = ? AND feed_item_id = ?", userID, feedItemID).First(&star).Error
	return star, err
}

func (r *Repo) CreateStar(star *model.FeedItemStar) error { return r.db.Create(star).Error }

func (r *Repo) DeleteStar(userID uuid.UUID, feedItemID uuid.UUID) error {
	return r.db.Where("user_id = ? AND feed_item_id = ?", userID, feedItemID).Delete(&model.FeedItemStar{}).Error
}

func (r *Repo) FindReadingListItem(userID uuid.UUID, feedItemID uuid.UUID) (model.ReadingListItem, error) {
	var item model.ReadingListItem
	err := r.db.Where("user_id = ? AND feed_item_id = ?", userID, feedItemID).First(&item).Error
	return item, err
}

func (r *Repo) CreateReadingListItem(item *model.ReadingListItem) error {
	return r.db.Create(item).Error
}

func (r *Repo) ListReadingListItems(userID uuid.UUID, limit int, offset int) ([]model.ReadingListItem, error) {
	var items []model.ReadingListItem
	err := r.db.Preload("FeedItem").Preload("FeedItem.FeedSource").
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&items).Error
	return items, err
}

func (r *Repo) CountReadingListItems(userID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.Model(&model.ReadingListItem{}).Where("user_id = ?", userID).Count(&count).Error
	return count, err
}

func (r *Repo) DeleteReadingListItem(userID uuid.UUID, feedItemID uuid.UUID) error {
	result := r.db.Where("user_id = ? AND feed_item_id = ?", userID, feedItemID).Delete(&model.ReadingListItem{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperr.NotFound("feed.reading_list_item_not_found", "Reading list item not found")
	}
	return nil
}
