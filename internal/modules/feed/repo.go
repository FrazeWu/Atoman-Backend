package feed

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

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
	err := r.db.Preload("User").Where("status = ?", "published").Where("user_id IN ?", userIDs).Find(&posts).Error
	return posts, err
}

func (r *Repo) ListPublishedPostsByChannelIDs(channelIDs []uuid.UUID) ([]model.Post, error) {
	if len(channelIDs) == 0 {
		return []model.Post{}, nil
	}
	var posts []model.Post
	err := r.db.Preload("User").Where("status = ?", "published").Where("channel_id IN ?", channelIDs).Find(&posts).Error
	return posts, err
}

func (r *Repo) ListPublishedPostsByCollectionIDs(collectionIDs []uuid.UUID) ([]model.Post, error) {
	if len(collectionIDs) == 0 {
		return []model.Post{}, nil
	}
	var posts []model.Post
	err := r.db.Preload("User").Joins("JOIN post_collections ON post_collections.post_id = posts.id").Where("posts.status = ?", "published").Where("post_collections.collection_id IN ?", collectionIDs).Find(&posts).Error
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
	err := r.db.Preload("User").Where("status = ?", "published").Order("created_at DESC").Offset(offset).Limit(limit).Find(&posts).Error
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
