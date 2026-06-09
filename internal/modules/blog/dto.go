package blog

import "github.com/google/uuid"

type CreatePostRequest struct {
	Title         string      `json:"title"`
	Content       string      `json:"content"`
	Excerpt       string      `json:"excerpt"`
	CoverURL      string      `json:"cover_url"`
	ChannelID     uuid.UUID   `json:"channel_id"`
	CollectionIDs []uuid.UUID `json:"collection_ids"`
	Visibility    string      `json:"visibility"`
	Status        string      `json:"status"`
}

type RatingSummary struct {
	AverageScore int     `json:"average_score"`
	AverageStars float64 `json:"average_stars"`
	RatingCount  int     `json:"rating_count"`
	MyScore      *int    `json:"my_score"`
}
