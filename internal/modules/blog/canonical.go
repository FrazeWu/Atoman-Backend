package blog

import (
	"errors"
	"fmt"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type canonicalBlogPostRow struct {
	ID                 uuid.UUID  `gorm:"column:id"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at"`
	AuthorID           *uuid.UUID `gorm:"column:author_id"`
	ChannelID          uuid.UUID  `gorm:"column:channel_id"`
	Title              string     `gorm:"column:title"`
	Summary            string     `gorm:"column:summary"`
	CoverURL           string     `gorm:"column:cover_url"`
	Status             string     `gorm:"column:status"`
	Visibility         string     `gorm:"column:visibility"`
	PublishedAt        *time.Time `gorm:"column:published_at"`
	ScheduledAt        *time.Time `gorm:"column:scheduled_at"`
	Content            string     `gorm:"column:content"`
	LanguageCode       string     `gorm:"column:language_code"`
	Pinned             bool       `gorm:"column:pinned"`
	ViewCount          int64      `gorm:"column:view_count"`
	CollectionConflict bool       `gorm:"column:collection_conflict"`
	CollectionID       *uuid.UUID `gorm:"column:collection_id"`
	CollectionPosition int        `gorm:"column:collection_position"`
}

func canonicalBlogPostsQuery(db *gorm.DB) *gorm.DB {
	return db.Table("content_entries AS posts").
		Select(`posts.id, posts.created_at, posts.updated_at, posts.author_id, posts.channel_id,
			posts.title, posts.summary, posts.cover_url, posts.status, posts.visibility,
			posts.published_at, posts.scheduled_at, blog_extensions.content,
			blog_extensions.language_code, blog_extensions.pinned, blog_extensions.view_count,
			blog_extensions.collection_conflict, memberships.collection_id,
			memberships.position AS collection_position`).
		Joins("JOIN content_blog_extensions AS blog_extensions ON blog_extensions.content_id = posts.id").
		Joins(`LEFT JOIN LATERAL (
			SELECT collection_id, position
			FROM content_collection_memberships
			WHERE content_id = posts.id
			ORDER BY position ASC, collection_id ASC
			LIMIT 1
		) AS memberships ON TRUE`).
		Where("posts.kind = ? AND posts.deleted_at IS NULL", "blog")
}

func CanonicalBlogPostsQuery(db *gorm.DB) *gorm.DB {
	return canonicalBlogPostsQuery(db)
}

func LoadCanonicalBlogPosts(db *gorm.DB, query *gorm.DB) ([]model.Post, error) {
	return loadCanonicalBlogPosts(db, query)
}

func loadCanonicalBlogPosts(db *gorm.DB, query *gorm.DB) ([]model.Post, error) {
	var rows []canonicalBlogPostRow
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return hydrateCanonicalBlogPosts(db, rows)
}

func loadCanonicalBlogPost(db *gorm.DB, postID uuid.UUID) (model.Post, error) {
	posts, err := loadCanonicalBlogPosts(db, canonicalBlogPostsQuery(db).Where("posts.id = ?", postID))
	if err != nil {
		return model.Post{}, err
	}
	if len(posts) == 0 {
		return model.Post{}, gorm.ErrRecordNotFound
	}
	return posts[0], nil
}

func hydrateCanonicalBlogPosts(db *gorm.DB, rows []canonicalBlogPostRow) ([]model.Post, error) {
	if len(rows) == 0 {
		return []model.Post{}, nil
	}

	authorIDs := make([]uuid.UUID, 0, len(rows))
	channelIDs := make([]uuid.UUID, 0, len(rows))
	collectionIDs := make([]uuid.UUID, 0, len(rows))
	authorSet := make(map[uuid.UUID]struct{})
	channelSet := make(map[uuid.UUID]struct{})
	collectionSet := make(map[uuid.UUID]struct{})
	for _, row := range rows {
		if row.AuthorID != nil && *row.AuthorID != uuid.Nil {
			if _, ok := authorSet[*row.AuthorID]; !ok {
				authorSet[*row.AuthorID] = struct{}{}
				authorIDs = append(authorIDs, *row.AuthorID)
			}
		}
		if row.ChannelID != uuid.Nil {
			if _, ok := channelSet[row.ChannelID]; !ok {
				channelSet[row.ChannelID] = struct{}{}
				channelIDs = append(channelIDs, row.ChannelID)
			}
		}
		if row.CollectionID != nil && *row.CollectionID != uuid.Nil {
			if _, ok := collectionSet[*row.CollectionID]; !ok {
				collectionSet[*row.CollectionID] = struct{}{}
				collectionIDs = append(collectionIDs, *row.CollectionID)
			}
		}
	}

	usersByID := make(map[uuid.UUID]model.User, len(authorIDs))
	if len(authorIDs) > 0 {
		var users []model.User
		if err := db.Where("uuid IN ?", authorIDs).Find(&users).Error; err != nil {
			return nil, err
		}
		for _, user := range users {
			usersByID[user.UUID] = user
		}
	}
	channelsByID := make(map[uuid.UUID]model.Channel, len(channelIDs))
	if len(channelIDs) > 0 {
		var channels []model.Channel
		if err := db.Preload("User").Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
			return nil, err
		}
		for _, channel := range channels {
			channelsByID[channel.ID] = channel
		}
	}
	collectionsByID := make(map[uuid.UUID]model.Collection, len(collectionIDs))
	if len(collectionIDs) > 0 {
		var collections []model.ContentCollection
		if err := db.Where("id IN ?", collectionIDs).Find(&collections).Error; err != nil {
			return nil, err
		}
		for _, collection := range collections {
			collectionsByID[collection.ID] = model.Collection{
				Base:        collection.Base,
				ChannelID:   collection.ChannelID,
				ContentType: "blog",
				CreatedBy:   collection.CreatedBy,
				Name:        collection.Name,
				Description: collection.Description,
				CoverURL:    collection.CoverURL,
				IsDefault:   collection.IsDefault,
			}
		}
	}

	posts := make([]model.Post, 0, len(rows))
	for _, row := range rows {
		post := model.Post{
			Base:  model.Base{ID: row.ID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt},
			Title: row.Title, Content: row.Content, Summary: row.Summary, LanguageCode: row.LanguageCode,
			CoverURL: row.CoverURL, Status: row.Status, Visibility: row.Visibility, Pinned: row.Pinned,
			ScheduledAt: row.ScheduledAt, PublishedAt: row.PublishedAt, ViewCount: row.ViewCount,
			CollectionConflict: row.CollectionConflict, CollectionPosition: row.CollectionPosition,
		}
		if row.AuthorID != nil {
			post.UserID = *row.AuthorID
			if user, ok := usersByID[*row.AuthorID]; ok {
				post.User = &user
			}
		}
		post.ChannelID = &row.ChannelID
		if channel, ok := channelsByID[row.ChannelID]; ok {
			post.Channel = &channel
		}
		if row.CollectionID != nil {
			post.CollectionID = row.CollectionID
			if collection, ok := collectionsByID[*row.CollectionID]; ok {
				post.Collection = &collection
			}
		}
		posts = append(posts, post)
	}
	return posts, nil
}

func canonicalBlogCollectionID(db *gorm.DB, collectionID uuid.UUID) (uuid.UUID, error) {
	var collection model.ContentCollection
	if err := db.First(&collection, "id = ?", collectionID).Error; err != nil {
		return uuid.Nil, err
	}
	return collection.ID, nil
}

func buildBlogVersionResponse(version model.ContentBlogVersion) model.BlogPostVersion {
	return model.BlogPostVersion{
		Base:         version.Base,
		PostID:       version.ContentID,
		Version:      version.Version,
		EditorID:     version.EditorID,
		Title:        version.Title,
		Content:      version.Content,
		Summary:      version.Summary,
		CoverURL:     version.CoverURL,
		Visibility:   version.Visibility,
		CollectionID: version.CollectionID,
		PublishedAt:  version.PublishedAt,
	}
}

func blogCollectionDTO(collection model.ContentCollection) model.Collection {
	return model.Collection{
		Base:        collection.Base,
		ChannelID:   collection.ChannelID,
		Channel:     collection.Channel,
		ContentType: "blog",
		CreatedBy:   collection.CreatedBy,
		Name:        collection.Name,
		Description: collection.Description,
		CoverURL:    collection.CoverURL,
		IsDefault:   collection.IsDefault,
	}
}

func blogCollectionDTOs(collections []model.ContentCollection) []model.Collection {
	result := make([]model.Collection, 0, len(collections))
	for _, collection := range collections {
		result = append(result, blogCollectionDTO(collection))
	}
	return result
}

func resolveBlogCollection(db *gorm.DB, userID, channelID uuid.UUID, requestedID *uuid.UUID, publishing bool) (*model.ContentCollection, error) {
	if channelID == uuid.Nil {
		return nil, fmt.Errorf("channel_id is required")
	}
	if requestedID != nil {
		var collection model.ContentCollection
		if err := db.Where("id = ? AND channel_id = ?", *requestedID, channelID).First(&collection).Error; err != nil {
			return nil, err
		}
		return &collection, nil
	}
	if !publishing {
		return nil, nil
	}
	var collection model.ContentCollection
	if err := db.Where("channel_id = ? AND is_default = ?", channelID, true).Order("created_at ASC, id ASC").First(&collection).Error; err == nil {
		return &collection, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	collection = model.ContentCollection{
		ChannelID:   channelID,
		CreatedBy:   &userID,
		Name:        "默认专栏",
		Description: "默认合集",
		IsDefault:   true,
	}
	if err := db.Create(&collection).Error; err != nil {
		return nil, err
	}
	return &collection, nil
}

func buildBlogDraftResponseFromCanonical(draft model.ContentBlogDraft) blogDraftResponse {
	var sourcePostID *string
	if draft.ContentID != nil {
		value := draft.ContentID.String()
		sourcePostID = &value
	}
	var channelID *string
	if draft.ChannelID != nil {
		value := draft.ChannelID.String()
		channelID = &value
	}
	var collectionID *string
	if draft.CollectionID != nil {
		value := draft.CollectionID.String()
		collectionID = &value
	}
	return blogDraftResponse{
		ID:           draft.ID,
		UserID:       draft.UserID,
		ContextKey:   draft.ContextKey,
		SourcePostID: sourcePostID,
		Title:        draft.Title,
		Content:      draft.Content,
		Summary:      draft.Summary,
		CoverURL:     draft.CoverURL,
		Visibility:   draft.Visibility,
		ChannelID:    channelID,
		CollectionID: collectionID,
		CreatedAt:    draft.CreatedAt,
		UpdatedAt:    draft.UpdatedAt,
	}
}
