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

type PostListItemDTO struct {
	PostDTO
	LikesCount     int64 `json:"likes_count"`
	CommentsCount  int64 `json:"comments_count"`
	BookmarksCount int64 `json:"bookmarks_count"`
}

type BookmarkPostDTO struct {
	PostDTO
	LikesCount    int64 `json:"likes_count"`
	CommentsCount int64 `json:"comments_count"`
}

type PostDTO struct {
	ID                 uuid.UUID                     `json:"id"`
	CreatedAt          time.Time                     `json:"created_at"`
	UpdatedAt          time.Time                     `json:"updated_at"`
	UserID             uuid.UUID                     `json:"user_id"`
	User               *model.User                   `json:"user,omitempty"`
	ChannelID          *uuid.UUID                    `json:"channel_id,omitempty"`
	Channel            *model.Channel                `json:"channel,omitempty"`
	CollectionID       *uuid.UUID                    `json:"collection_id,omitempty"`
	Collection         *model.Collection             `json:"collection,omitempty"`
	Collections        []model.Collection            `json:"collections,omitempty"`
	CollectionPosition int                           `json:"collection_position"`
	CollectionConflict bool                          `json:"collection_conflict"`
	Title              string                        `json:"title"`
	Content            string                        `json:"content"`
	Summary            string                        `json:"summary"`
	LanguageCode       string                        `json:"language_code"`
	CoverURL           string                        `json:"cover_url"`
	Status             string                        `json:"status"`
	Visibility         string                        `json:"visibility"`
	Pinned             bool                          `json:"pinned"`
	ScheduledAt        *time.Time                    `json:"scheduled_at,omitempty"`
	PublishedAt        *time.Time                    `json:"published_at,omitempty"`
	ViewCount          int64                         `json:"view_count"`
	BookmarksCount     int64                         `json:"bookmarks_count"`
	RatingScore        float64                       `json:"rating_score"`
	RatingCount        int64                         `json:"rating_count"`
	ViewerRating       *int                          `json:"viewer_rating,omitempty"`
	References         []reference.ResolvedReference `json:"references"`
}

func newPostDTO(post model.Post) PostDTO {
	return PostDTO{
		ID: post.ID, CreatedAt: post.CreatedAt, UpdatedAt: post.UpdatedAt,
		UserID: post.UserID, User: post.User, ChannelID: post.ChannelID, Channel: post.Channel,
		CollectionID: post.CollectionID, Collection: post.Collection, Collections: post.Collections,
		CollectionPosition: post.CollectionPosition, CollectionConflict: post.CollectionConflict,
		Title: post.Title, Content: post.Content, Summary: post.Summary, LanguageCode: post.LanguageCode,
		CoverURL: post.CoverURL, Status: post.Status, Visibility: post.Visibility, Pinned: post.Pinned,
		ScheduledAt: post.ScheduledAt, PublishedAt: post.PublishedAt, ViewCount: post.ViewCount,
		BookmarksCount: post.BookmarksCount, RatingScore: post.RatingScore, RatingCount: post.RatingCount,
		ViewerRating: post.ViewerRating, References: []reference.ResolvedReference{},
	}
}

type BookmarkListItemDTO struct {
	model.Bookmark
	Post *BookmarkPostDTO `json:"post,omitempty"`
}
