package handlers

import (
	"net/http"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GetSubscribedVideos returns public videos from the user's subscribed
// channels and video collections.
func GetSubscribedVideos(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		channelIDs := db.Model(&model.FeedSource{}).
			Select("feed_sources.source_id").
			Joins("JOIN subscriptions ON subscriptions.feed_source_id = feed_sources.id").
			Where("subscriptions.user_id = ? AND subscriptions.deleted_at IS NULL", userID).
			Where("feed_sources.source_type = ? AND feed_sources.source_id IS NOT NULL", "internal_channel")
		collectionIDs := db.Model(&model.FeedSource{}).
			Select("feed_sources.source_id").
			Joins("JOIN subscriptions ON subscriptions.feed_source_id = feed_sources.id").
			Joins("JOIN collections ON collections.id = feed_sources.source_id AND collections.content_type = ?", "video").
			Where("subscriptions.user_id = ? AND subscriptions.deleted_at IS NULL", userID).
			Where("feed_sources.source_type = ? AND feed_sources.source_id IS NOT NULL", "internal_collection")

		var videos []model.Video
		query := db.Preload("Channel").Preload("Collections").Preload("Tags").
			Where("videos.status = ? AND videos.visibility = ?", "published", "public").
			Where("(videos.channel_id IN (?) OR EXISTS (SELECT 1 FROM video_collections vc WHERE vc.video_id = videos.id AND vc.collection_id IN (?)) )", channelIDs, collectionIDs).
			Order("videos.created_at DESC, videos.id DESC")
		if err := query.Find(&videos).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch subscribed videos"})
			return
		}
		c.JSON(http.StatusOK, videos)
	}
}

type videoCollectionBookmarkResponse struct {
	ID         uuid.UUID         `json:"id"`
	Collection *model.Collection `json:"collection,omitempty"`
}

// GetVideoCollectionBookmarks returns the user's subscribed video collections.
func GetVideoCollectionBookmarks(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var subscriptions []model.Subscription
		if err := db.Preload("FeedSource").
			Where("subscriptions.user_id = ? AND subscriptions.deleted_at IS NULL", userID).
			Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
			Where("feed_sources.source_type = ?", "internal_collection").
			Order("subscriptions.created_at DESC").Find(&subscriptions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch collection bookmarks"})
			return
		}

		collectionIDs := make([]uuid.UUID, 0, len(subscriptions))
		for _, subscription := range subscriptions {
			if subscription.FeedSource != nil && subscription.FeedSource.SourceID != nil {
				collectionIDs = append(collectionIDs, *subscription.FeedSource.SourceID)
			}
		}
		collectionsByID := make(map[uuid.UUID]*model.Collection, len(collectionIDs))
		if len(collectionIDs) > 0 {
			var collections []model.Collection
			if err := db.Where("id IN (?) AND content_type = ?", collectionIDs, "video").Find(&collections).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch collection bookmarks"})
				return
			}
			for index := range collections {
				collection := collections[index]
				collectionsByID[collection.ID] = &collection
			}
		}

		items := make([]videoCollectionBookmarkResponse, 0, len(subscriptions))
		for _, subscription := range subscriptions {
			if subscription.FeedSource == nil || subscription.FeedSource.SourceID == nil {
				continue
			}
			collection := collectionsByID[*subscription.FeedSource.SourceID]
			if collection == nil {
				continue
			}
			items = append(items, videoCollectionBookmarkResponse{ID: subscription.ID, Collection: collection})
		}
		c.JSON(http.StatusOK, gin.H{"data": items, "message": "ok"})
	}
}
