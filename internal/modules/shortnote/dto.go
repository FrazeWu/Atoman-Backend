package shortnote

import (
	"time"

	"github.com/google/uuid"
)

type noteInput struct {
	Content   string   `json:"content"`
	MediaURLs []string `json:"media_urls"`
}

type NoteDTO struct {
	ID            uuid.UUID  `json:"id"`
	UserID        uuid.UUID  `json:"user_id"`
	Content       string     `json:"content"`
	Media         []MediaDTO `json:"media"`
	LikesCount    int64      `json:"likes_count"`
	CommentsCount int        `json:"comments_count"`
	Edited        bool       `json:"edited"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type MediaDTO struct {
	ID       uuid.UUID `json:"id"`
	URL      string    `json:"url"`
	Position int       `json:"position"`
}
