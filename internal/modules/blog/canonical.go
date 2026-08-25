package blog

import (
	"errors"
	"fmt"
	"time"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"

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
	capabilities := contentmodule.CurrentMediaSchema(db)
	query := db.Table("content_entries AS posts").
		Select(`posts.id, posts.created_at, posts.updated_at, posts.author_id, posts.channel_id,
			posts.title, posts.summary, posts.cover_url, posts.status, posts.visibility,
			posts.published_at, posts.scheduled_at, blog_extensions.content,
			blog_extensions.language_code, blog_extensions.pinned, blog_extensions.view_count,
			blog_extensions.collection_conflict, memberships.collection_id,
			memberships.position AS collection_position`).
		Joins("JOIN content_blog_extensions AS blog_extensions ON blog_extensions.content_id = posts.id").
		Where("posts.kind = ? AND posts.deleted_at IS NULL", "blog")
	if !capabilities.ContentCollectionMembershipTable {
		query = db.Table("content_entries AS posts").
			Select(`posts.id, posts.created_at, posts.updated_at, posts.author_id, posts.channel_id,
				posts.title, posts.summary, posts.cover_url, posts.status, posts.visibility,
				posts.published_at, posts.scheduled_at, blog_extensions.content,
				blog_extensions.language_code, blog_extensions.pinned, blog_extensions.view_count,
				blog_extensions.collection_conflict, NULL::uuid AS collection_id,
				0 AS collection_position`).
			Joins("JOIN content_blog_extensions AS blog_extensions ON blog_extensions.content_id = posts.id").
			Where("posts.kind = ? AND posts.deleted_at IS NULL", "blog")
		return query
	}
	return query.Joins(`LEFT JOIN LATERAL (
		SELECT collection_id, position
		FROM content_collection_memberships
		WHERE content_id = posts.id
		ORDER BY position ASC, collection_id ASC
		LIMIT 1
	) AS memberships ON TRUE`)
}

func CanonicalBlogPostsQuery(db *gorm.DB) *gorm.DB {
	return canonicalBlogPostsQuery(db)
}

func LoadCanonicalBlogContents(db *gorm.DB, query *gorm.DB) ([]BlogContent, error) {
	var rows []canonicalBlogPostRow
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return hydrateCanonicalBlogContents(db, rows)
}

// LoadCanonicalBlogPosts preserves the legacy feed projection while callers
// migrate to BlogContent.
func LoadCanonicalBlogPosts(db *gorm.DB, query *gorm.DB) ([]model.Post, error) {
	contents, err := LoadCanonicalBlogContents(db, query)
	if err != nil {
		return nil, err
	}

	posts := make([]model.Post, 0, len(contents))
	for _, content := range contents {
		posts = append(posts, model.Post{
			Base:               model.Base{ID: content.ID, CreatedAt: content.CreatedAt, UpdatedAt: content.UpdatedAt},
			UserID:             content.UserID,
			User:               content.User,
			ChannelID:          content.ChannelID,
			Channel:            content.Channel,
			CollectionID:       content.CollectionID,
			Collection:         legacyBlogCollection(content.Collection),
			Collections:        legacyBlogCollections(content.Collections),
			CollectionPosition: content.CollectionPosition,
			CollectionConflict: content.CollectionConflict,
			Title:              content.Title,
			Content:            content.Content,
			Summary:            content.Summary,
			LanguageCode:       content.LanguageCode,
			CoverURL:           content.CoverURL,
			Status:             content.Status,
			Visibility:         content.Visibility,
			Pinned:             content.Pinned,
			ScheduledAt:        content.ScheduledAt,
			PublishedAt:        content.PublishedAt,
			ViewCount:          content.ViewCount,
			BookmarksCount:     content.BookmarksCount,
			RatingScore:        content.RatingScore,
			RatingCount:        content.RatingCount,
			ViewerRating:       content.ViewerRating,
		})
	}
	return posts, nil
}

func legacyBlogCollection(collection *BlogCollection) *model.Collection {
	if collection == nil {
		return nil
	}
	return &model.Collection{
		Base:        model.Base{ID: collection.ID, CreatedAt: collection.CreatedAt, UpdatedAt: collection.UpdatedAt},
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

func legacyBlogCollections(collections []BlogCollection) []model.Collection {
	if len(collections) == 0 {
		return nil
	}
	legacy := make([]model.Collection, 0, len(collections))
	for index := range collections {
		legacy = append(legacy, *legacyBlogCollection(&collections[index]))
	}
	return legacy
}

func hydrateCanonicalBlogContents(db *gorm.DB, rows []canonicalBlogPostRow) ([]BlogContent, error) {
	if len(rows) == 0 {
		return []BlogContent{}, nil
	}
	userIDs := make([]uuid.UUID, 0, len(rows))
	channelIDs := make([]uuid.UUID, 0, len(rows))
	collectionIDs := make([]uuid.UUID, 0, len(rows))
	usersSeen, channelsSeen, collectionsSeen := map[uuid.UUID]struct{}{}, map[uuid.UUID]struct{}{}, map[uuid.UUID]struct{}{}
	for _, row := range rows {
		if row.AuthorID != nil && *row.AuthorID != uuid.Nil {
			if _, ok := usersSeen[*row.AuthorID]; !ok {
				usersSeen[*row.AuthorID] = struct{}{}
				userIDs = append(userIDs, *row.AuthorID)
			}
		}
		if row.ChannelID != uuid.Nil {
			if _, ok := channelsSeen[row.ChannelID]; !ok {
				channelsSeen[row.ChannelID] = struct{}{}
				channelIDs = append(channelIDs, row.ChannelID)
			}
		}
		if row.CollectionID != nil && *row.CollectionID != uuid.Nil {
			if _, ok := collectionsSeen[*row.CollectionID]; !ok {
				collectionsSeen[*row.CollectionID] = struct{}{}
				collectionIDs = append(collectionIDs, *row.CollectionID)
			}
		}
	}
	users := map[uuid.UUID]model.User{}
	if len(userIDs) > 0 {
		var values []model.User
		if err := db.Where("uuid IN ?", userIDs).Find(&values).Error; err != nil {
			return nil, err
		}
		for _, value := range values {
			users[value.UUID] = value
		}
	}
	channels := map[uuid.UUID]model.Channel{}
	if len(channelIDs) > 0 {
		var values []model.Channel
		if err := db.Preload("User").Where("id IN ?", channelIDs).Find(&values).Error; err != nil {
			return nil, err
		}
		for _, value := range values {
			channels[value.ID] = value
		}
	}
	collections := map[uuid.UUID]BlogCollection{}
	if len(collectionIDs) > 0 {
		var values []model.ContentCollection
		if err := db.Where("id IN ?", collectionIDs).Find(&values).Error; err != nil {
			return nil, err
		}
		for _, value := range values {
			collections[value.ID] = BlogCollection{ID: value.ID, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, ChannelID: value.ChannelID, CreatedBy: value.CreatedBy, Name: value.Name, Description: value.Description, CoverURL: value.CoverURL, IsDefault: value.IsDefault}
		}
	}
	contents := make([]BlogContent, 0, len(rows))
	for _, row := range rows {
		content := BlogContent{ID: row.ID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ChannelID: &row.ChannelID, Title: row.Title, Content: row.Content, Summary: row.Summary, LanguageCode: row.LanguageCode, CoverURL: row.CoverURL, Status: row.Status, Visibility: row.Visibility, Pinned: row.Pinned, ScheduledAt: row.ScheduledAt, PublishedAt: row.PublishedAt, ViewCount: row.ViewCount, CollectionConflict: row.CollectionConflict, CollectionPosition: row.CollectionPosition}
		if row.AuthorID != nil {
			content.UserID = *row.AuthorID
			if value, ok := users[*row.AuthorID]; ok {
				content.User = &value
			}
		}
		if value, ok := channels[row.ChannelID]; ok {
			content.Channel = &value
		}
		if row.CollectionID != nil {
			content.CollectionID = row.CollectionID
			if value, ok := collections[*row.CollectionID]; ok {
				content.Collection = &value
			}
		}
		contents = append(contents, content)
	}
	return contents, nil
}

func loadCanonicalBlogContent(db *gorm.DB, contentID uuid.UUID) (BlogContent, error) {
	contents, err := LoadCanonicalBlogContents(db, canonicalBlogPostsQuery(db).Where("posts.id = ?", contentID))
	if err != nil {
		return BlogContent{}, err
	}
	if len(contents) == 0 {
		return BlogContent{}, gorm.ErrRecordNotFound
	}
	return contents[0], nil
}

func blogCollectionFromContentCollection(collection model.ContentCollection) BlogCollection {
	return BlogCollection{
		ID: collection.ID, CreatedAt: collection.CreatedAt, UpdatedAt: collection.UpdatedAt,
		ChannelID: collection.ChannelID, Channel: collection.Channel, CreatedBy: collection.CreatedBy,
		Name: collection.Name, Description: collection.Description, CoverURL: collection.CoverURL, IsDefault: collection.IsDefault,
	}
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
		Name:        "默认合集",
		Description: "默认合集",
		IsDefault:   true,
	}
	if err := db.Create(&collection).Error; err != nil {
		return nil, err
	}
	return &collection, nil
}

func buildBlogDraftResponseFromCanonical(draft model.ContentBlogDraft) blogDraftResponse {
	var sourceContentID *string
	if draft.ContentID != nil {
		value := draft.ContentID.String()
		sourceContentID = &value
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
		ID:              draft.ID,
		UserID:          draft.UserID,
		ContextKey:      draft.ContextKey,
		SourceContentID: sourceContentID,
		Title:           draft.Title,
		Content:         draft.Content,
		Summary:         draft.Summary,
		CoverURL:        draft.CoverURL,
		Visibility:      draft.Visibility,
		ChannelID:       channelID,
		CollectionID:    collectionID,
		CreatedAt:       draft.CreatedAt,
		UpdatedAt:       draft.UpdatedAt,
	}
}
