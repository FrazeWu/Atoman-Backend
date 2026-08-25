package blog

import (
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/reference"

	"github.com/google/uuid"
)

type SEOPostDTO struct {
	ID          uuid.UUID  `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	ImageURL    string     `json:"image_url"`
	AuthorName  string     `json:"author_name"`
	PublishedAt *time.Time `json:"published_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Path        string     `json:"path"`
}

type SEOSitemapItemDTO struct {
	Path         string    `json:"path"`
	LastModified time.Time `json:"last_modified"`
}

type SEOPostResponse struct {
	Data SEOPostDTO `json:"data"`
}

type SEOSitemapResponse struct {
	Data []SEOSitemapItemDTO `json:"data"`
}

type CreatePostRequest struct {
	Title        string    `json:"title"`
	Content      string    `json:"content"`
	Excerpt      string    `json:"excerpt"`
	Summary      string    `json:"summary"`
	CoverURL     string    `json:"cover_url"`
	ChannelID    uuid.UUID `json:"channel_id"`
	CollectionID uuid.UUID `json:"collection_id"`
	Visibility   string    `json:"visibility"`
	Status       string    `json:"status"`
}

type RecommendationItemDTO struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	ContentType   string `json:"content_type"`
	ImageURL      string `json:"image_url"`
	TargetPath    string `json:"target_path"`
	ScoreLabel    string `json:"score_label"`
	LikesCount    int64  `json:"likes_count"`
	CommentsCount int64  `json:"comments_count"`
}

type BlogContentListItemDTO struct {
	BlogContentDTO
	LikesCount     int64 `json:"likes_count"`
	CommentsCount  int64 `json:"comments_count"`
	BookmarksCount int64 `json:"bookmarks_count"`
}

type BookmarkBlogContentDTO struct {
	BlogContentDTO
	LikesCount    int64 `json:"likes_count"`
	CommentsCount int64 `json:"comments_count"`
}

type BlogCollectionDTO struct {
	ID          uuid.UUID      `json:"id"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	ChannelID   uuid.UUID      `json:"channel_id"`
	Channel     *model.Channel `json:"channel,omitempty"`
	CreatedBy   *uuid.UUID     `json:"created_by,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	CoverURL    string         `json:"cover_url"`
	IsDefault   bool           `json:"is_default"`
}

type BlogContentDTO struct {
	ID                   uuid.UUID                     `json:"id"`
	CreatedAt            time.Time                     `json:"created_at"`
	UpdatedAt            time.Time                     `json:"updated_at"`
	UserID               uuid.UUID                     `json:"user_id"`
	User                 *model.User                   `json:"user,omitempty"`
	ChannelID            *uuid.UUID                    `json:"channel_id,omitempty"`
	Channel              *model.Channel                `json:"channel,omitempty"`
	CollectionID         *uuid.UUID                    `json:"collection_id,omitempty"`
	Collection           *BlogCollectionDTO            `json:"collection,omitempty"`
	Collections          []BlogCollectionDTO           `json:"collections,omitempty"`
	CollectionPosition   int                           `json:"collection_position"`
	CollectionConflict   bool                          `json:"collection_conflict"`
	Title                string                        `json:"title"`
	Content              string                        `json:"content"`
	Summary              string                        `json:"summary"`
	LanguageCode         string                        `json:"language_code"`
	CoverURL             string                        `json:"cover_url"`
	Status               string                        `json:"status"`
	Visibility           string                        `json:"visibility"`
	Pinned               bool                          `json:"pinned"`
	ScheduledAt          *time.Time                    `json:"scheduled_at,omitempty"`
	PublishedAt          *time.Time                    `json:"published_at,omitempty"`
	ViewCount            int64                         `json:"view_count"`
	BookmarksCount       int64                         `json:"bookmarks_count"`
	RatingScore          float64                       `json:"rating_score"`
	RatingCount          int64                         `json:"rating_count"`
	ViewerRating         *int                          `json:"viewer_rating,omitempty"`
	WeightedRatingScore  *float64                      `json:"weighted_rating_score,omitempty"`
	WeightedRatingCount  int                           `json:"weighted_rating_count"`
	WeightedRatingActive bool                          `json:"weighted_rating_active"`
	References           []reference.ResolvedReference `json:"references"`
}

func newBlogContentDTOFromCanonical(content BlogContent) BlogContentDTO {
	var collection *BlogCollectionDTO
	if content.Collection != nil {
		collection = &BlogCollectionDTO{ID: content.Collection.ID, CreatedAt: content.Collection.CreatedAt, UpdatedAt: content.Collection.UpdatedAt, ChannelID: content.Collection.ChannelID, Channel: content.Collection.Channel, CreatedBy: content.Collection.CreatedBy, Name: content.Collection.Name, Description: content.Collection.Description, CoverURL: content.Collection.CoverURL, IsDefault: content.Collection.IsDefault}
	}
	collections := make([]BlogCollectionDTO, 0, len(content.Collections))
	for _, item := range content.Collections {
		collections = append(collections, BlogCollectionDTO{ID: item.ID, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, ChannelID: item.ChannelID, Channel: item.Channel, CreatedBy: item.CreatedBy, Name: item.Name, Description: item.Description, CoverURL: item.CoverURL, IsDefault: item.IsDefault})
	}
	return BlogContentDTO{ID: content.ID, CreatedAt: content.CreatedAt, UpdatedAt: content.UpdatedAt, UserID: content.UserID, User: content.User, ChannelID: content.ChannelID, Channel: content.Channel, CollectionID: content.CollectionID, Collection: collection, Collections: collections, CollectionPosition: content.CollectionPosition, CollectionConflict: content.CollectionConflict, Title: content.Title, Content: content.Content, Summary: content.Summary, LanguageCode: content.LanguageCode, CoverURL: content.CoverURL, Status: content.Status, Visibility: content.Visibility, Pinned: content.Pinned, ScheduledAt: content.ScheduledAt, PublishedAt: content.PublishedAt, ViewCount: content.ViewCount, BookmarksCount: content.BookmarksCount, RatingScore: content.RatingScore, RatingCount: content.RatingCount, ViewerRating: content.ViewerRating, WeightedRatingScore: content.WeightedRatingScore, WeightedRatingCount: content.WeightedRatingCount, WeightedRatingActive: content.WeightedRatingActive, References: []reference.ResolvedReference{}}
}

type BlogContentVersionDTO struct {
	ID           uuid.UUID  `json:"id"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ContentID    uuid.UUID  `json:"content_id"`
	Version      int        `json:"version"`
	EditorID     uuid.UUID  `json:"editor_id"`
	Title        string     `json:"title"`
	Content      string     `json:"content"`
	Summary      string     `json:"summary"`
	CoverURL     string     `json:"cover_url"`
	Visibility   string     `json:"visibility"`
	CollectionID uuid.UUID  `json:"content_collection_id"`
	PublishedAt  *time.Time `json:"published_at,omitempty"`
}

func newBlogContentVersionDTO(version model.ContentBlogVersion) BlogContentVersionDTO {
	return BlogContentVersionDTO{
		ID: version.ID, CreatedAt: version.CreatedAt, UpdatedAt: version.UpdatedAt,
		ContentID: version.ContentID, Version: version.Version, EditorID: version.EditorID,
		Title: version.Title, Content: version.Content, Summary: version.Summary,
		CoverURL: version.CoverURL, Visibility: version.Visibility, CollectionID: version.CollectionID,
		PublishedAt: version.PublishedAt,
	}
}

type BlogBookmarkDTO struct {
	ID               uuid.UUID  `json:"id"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	UserID           uuid.UUID  `json:"user_id"`
	ContentID        uuid.UUID  `json:"content_id"`
	BookmarkFolderID *uuid.UUID `json:"bookmark_folder_id,omitempty"`
}

func newBlogBookmarkDTO(bookmark model.Bookmark) BlogBookmarkDTO {
	return BlogBookmarkDTO{
		ID: bookmark.ID, CreatedAt: bookmark.CreatedAt, UpdatedAt: bookmark.UpdatedAt,
		UserID: bookmark.UserID, ContentID: bookmark.ContentID, BookmarkFolderID: bookmark.BookmarkFolderID,
	}
}

type BookmarkListItemDTO struct {
	BlogBookmarkDTO
	Content *BookmarkBlogContentDTO `json:"content,omitempty"`
}
