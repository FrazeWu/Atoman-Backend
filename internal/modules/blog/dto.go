package blog

import (
	"time"

	"atoman/internal/model"

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
	ID          string `json:"id"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	ContentType string `json:"content_type"`
	ImageURL    string `json:"image_url"`
	TargetPath  string `json:"target_path"`
	ScoreLabel  string `json:"score_label"`
}

type PostListItemDTO struct {
	model.Post
	LikesCount     int64 `json:"likes_count"`
	CommentsCount  int64 `json:"comments_count"`
	BookmarksCount int64 `json:"bookmarks_count"`
}
