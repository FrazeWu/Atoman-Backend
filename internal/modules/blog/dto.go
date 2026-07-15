package blog

import (
	"atoman/internal/model"

	"github.com/google/uuid"
)

type CreatePostRequest struct {
	Title         string      `json:"title"`
	Content       string      `json:"content"`
	Excerpt       string      `json:"excerpt"`
	Summary       string      `json:"summary"`
	CoverURL      string      `json:"cover_url"`
	ChannelID     uuid.UUID   `json:"channel_id"`
	CollectionID  uuid.UUID   `json:"collection_id"`
	CollectionIDs []uuid.UUID `json:"collection_ids" swaggerignore:"true"`
	Visibility    string      `json:"visibility"`
	Status        string      `json:"status"`
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
	model.Post
	LikesCount     int64 `json:"likes_count"`
	CommentsCount  int64 `json:"comments_count"`
	BookmarksCount int64 `json:"bookmarks_count"`
}

type BookmarkPostDTO struct {
	model.Post
	LikesCount    int64 `json:"likes_count"`
	CommentsCount int64 `json:"comments_count"`
}

type BookmarkListItemDTO struct {
	model.Bookmark
	Post *BookmarkPostDTO `json:"post,omitempty"`
}
