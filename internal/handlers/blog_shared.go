package handlers

import (
	"crypto/sha256"
	"fmt"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BlogDraftResponse struct {
	ID            uuid.UUID `json:"id"`
	UserID        uuid.UUID `json:"user_id"`
	ContextKey    string    `json:"context_key"`
	SourcePostID  *string   `json:"source_post_id,omitempty"`
	Title         string    `json:"title"`
	Content       string    `json:"content"`
	Summary       string    `json:"summary"`
	CoverURL      string    `json:"cover_url"`
	Visibility    string    `json:"visibility"`
	AllowComments bool      `json:"allow_comments"`
	ChannelID     *string   `json:"channel_id,omitempty"`
	CollectionIDs []string  `json:"collection_ids"`
	CreatedAt     any       `json:"created_at"`
	UpdatedAt     any       `json:"updated_at"`
}

func currentBlogViewerID(c *gin.Context) *uuid.UUID {
	userIDVal, exists := c.Get("user_id")
	if !exists {
		return nil
	}
	userID, ok := userIDVal.(uuid.UUID)
	if !ok {
		return nil
	}
	return &userID
}

func RequireBlogPostEditAccess(db *gorm.DB, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(401, gin.H{"error": "Unauthorized"})
			return
		}
		userID, ok := userIDVal.(uuid.UUID)
		if !ok {
			c.JSON(401, gin.H{"error": "Unauthorized"})
			return
		}

		postID := c.Param("roomID")
		if postID == "" {
			postID = c.Param("id")
		}
		if postID == "" {
			c.JSON(400, gin.H{"error": "missing post id"})
			return
		}

		var post model.Post
		if err := db.Select("id", "user_id").First(&post, "id = ?", postID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(404, gin.H{"error": "Post not found"})
				return
			}
			c.JSON(500, gin.H{"error": "Failed to load post"})
			return
		}
		if post.UserID != userID {
			c.JSON(403, gin.H{"error": "You don't have permission to edit this post"})
			return
		}

		h(c)
	}
}

func canViewPublishedBlogPost(db *gorm.DB, viewerID *uuid.UUID, post model.Post) (bool, error) {
	switch post.Visibility {
	case "", "public":
		return true, nil
	case "private":
		return viewerID != nil && post.UserID == *viewerID, nil
	case "followers":
		if viewerID == nil {
			return false, nil
		}
		if post.UserID == *viewerID {
			return true, nil
		}
		if post.ChannelID == nil {
			return false, nil
		}

		hash := fmt.Sprintf("%x", sha256.Sum256([]byte("internal_channel:"+post.ChannelID.String())))
		var source model.FeedSource
		if err := db.Where("hash = ?", hash).First(&source).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return false, nil
			}
			return false, err
		}

		var sub model.Subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", *viewerID, source.ID).First(&sub).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return false, nil
			}
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}
