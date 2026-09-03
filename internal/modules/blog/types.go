package blog

import (
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

// BlogCollection is the canonical runtime projection of a content collection.
type BlogCollection struct {
	ID          uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ChannelID   uuid.UUID
	Channel     *model.Channel
	CreatedBy   *uuid.UUID
	Name        string
	Description string
	CoverURL    string
	IsDefault   bool
}

// BlogContent is the canonical runtime projection of a blog content entry.
// BlogContent is the canonical runtime projection of a blog content entry.
type BlogContent struct {
	ID                   uuid.UUID
	CreatedAt            time.Time
	UpdatedAt            time.Time
	UserID               uuid.UUID
	User                 *model.User
	ChannelID            *uuid.UUID
	Channel              *model.Channel
	CollectionID         *uuid.UUID
	Collection           *BlogCollection
	Collections          []BlogCollection
	CollectionPosition   int
	CollectionConflict   bool
	Title                string
	Content              string
	Summary              string
	LanguageCode         string
	CoverURL             string
	Status               string
	Visibility           string
	Pinned               bool
	ScheduledAt          *time.Time
	PublishedAt          *time.Time
	ViewCount            int64
	BookmarksCount       int64
	RatingScore          float64
	RatingCount          int64
	ViewerRating         *int
	WeightedRatingScore  *float64
	WeightedRatingCount  int
	WeightedRatingActive bool
	Tags                 []string
}
