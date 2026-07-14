package migrations

import (
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type legacyBlogPostRating struct {
	model.Base
	PostID uuid.UUID `gorm:"type:uuid;not null;index"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index"`
	Score  int       `gorm:"not null"`
}

func (legacyBlogPostRating) TableName() string { return "blog_post_ratings" }

func RunBlogSingleCollectionMigration(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if !tx.Migrator().HasTable(&model.Post{}) {
			return tx.AutoMigrate(&model.BlogPostVersion{})
		}
		if err := tx.AutoMigrate(&model.Post{}, &model.BlogDraft{}, &model.BlogPostVersion{}); err != nil {
			return err
		}

		linksByPost := map[uuid.UUID][]model.PostCollection{}
		if tx.Migrator().HasTable(&model.PostCollection{}) {
			var links []model.PostCollection
			if err := tx.Order("position ASC, collection_id ASC").Find(&links).Error; err != nil {
				return err
			}
			for _, link := range links {
				linksByPost[link.PostID] = append(linksByPost[link.PostID], link)
			}
		}

		var posts []model.Post
		if err := tx.Find(&posts).Error; err != nil {
			return err
		}
		for _, post := range posts {
			updates := map[string]any{}
			if post.CollectionID == nil || *post.CollectionID == uuid.Nil {
				collection, position, err := migrationCollectionForPost(tx, post, linksByPost[post.ID])
				if err != nil {
					return err
				}
				updates["collection_id"] = collection.ID
				updates["collection_position"] = position
				updates["channel_id"] = collection.ChannelID
			}
			if post.Status == "published" && post.PublishedAt == nil {
				updates["published_at"] = post.CreatedAt
			}
			if len(updates) > 0 {
				if err := tx.Model(&model.Post{}).Where("id = ?", post.ID).Updates(updates).Error; err != nil {
					return err
				}
			}
		}

		if tx.Migrator().HasTable(&model.BlogDraft{}) {
			var drafts []model.BlogDraft
			if err := tx.Find(&drafts).Error; err != nil {
				return err
			}
			for _, draft := range drafts {
				if draft.CollectionID != nil || strings.TrimSpace(draft.CollectionIDs) == "" {
					continue
				}
				for _, raw := range strings.Split(draft.CollectionIDs, ",") {
					id, err := uuid.Parse(strings.TrimSpace(raw))
					if err != nil {
						continue
					}
					if err := tx.Model(&model.BlogDraft{}).Where("id = ?", draft.ID).Update("collection_id", id).Error; err != nil {
						return err
					}
					break
				}
			}
		}

		if tx.Migrator().HasTable(&model.PostCollection{}) {
			if err := tx.Migrator().DropTable(&model.PostCollection{}); err != nil {
				return err
			}
		}
		if tx.Migrator().HasTable(&legacyBlogPostRating{}) {
			if err := tx.Migrator().DropTable(&legacyBlogPostRating{}); err != nil {
				return err
			}
		}
		for _, field := range []string{"RatingAverageScore", "RatingCount"} {
			if tx.Migrator().HasColumn(&model.Post{}, field) {
				if err := tx.Migrator().DropColumn(&model.Post{}, field); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func migrationCollectionForPost(tx *gorm.DB, post model.Post, links []model.PostCollection) (model.Collection, int, error) {
	for _, link := range links {
		var collection model.Collection
		if err := tx.First(&collection, "id = ?", link.CollectionID).Error; err == nil {
			if post.ChannelID == nil || collection.ChannelID == *post.ChannelID {
				return collection, link.Position, nil
			}
		} else if err != gorm.ErrRecordNotFound {
			return model.Collection{}, 0, err
		}
	}

	channelID := post.ChannelID
	if channelID == nil || *channelID == uuid.Nil {
		channel, err := migrationDefaultChannelForPost(tx, post)
		if err != nil {
			return model.Collection{}, 0, err
		}
		channelID = &channel.ID
	}
	var collection model.Collection
	if err := tx.Where("channel_id = ? AND is_default = ?", *channelID, true).First(&collection).Error; err == nil {
		return collection, migrationNextPosition(tx, collection.ID), nil
	} else if err != gorm.ErrRecordNotFound {
		return model.Collection{}, 0, err
	}
	collection = model.Collection{ChannelID: *channelID, CreatedBy: &post.UserID, Name: "默认专栏", Description: "默认合集", IsDefault: true}
	if err := tx.Create(&collection).Error; err != nil {
		return model.Collection{}, 0, err
	}
	return collection, migrationNextPosition(tx, collection.ID), nil
}

func migrationDefaultChannelForPost(tx *gorm.DB, post model.Post) (model.Channel, error) {
	var channel model.Channel
	if err := tx.Where("user_id = ? AND content_type = ? AND is_default = ?", post.UserID, model.ChannelContentTypeBlog, true).First(&channel).Error; err == nil {
		return channel, nil
	} else if err != gorm.ErrRecordNotFound {
		return model.Channel{}, err
	}
	channel = model.Channel{UserID: &post.UserID, Name: "文章", Slug: "posts-" + post.UserID.String(), ContentType: model.ChannelContentTypeBlog, IsDefault: true}
	if err := tx.Create(&channel).Error; err != nil {
		return model.Channel{}, err
	}
	return channel, nil
}

func migrationNextPosition(tx *gorm.DB, collectionID uuid.UUID) int {
	var maxPosition int
	if err := tx.Model(&model.Post{}).Where("collection_id = ?", collectionID).Select("COALESCE(MAX(collection_position), -1)").Scan(&maxPosition).Error; err != nil {
		return 0
	}
	return maxPosition + 1
}
