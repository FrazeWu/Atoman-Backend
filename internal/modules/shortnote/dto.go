package shortnote

import (
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

type noteInput struct {
	Content   string   `json:"content"`
	MediaURLs []string `json:"media_urls"`
}

type voteInput struct {
	Direction string `json:"direction"`
}

type NoteDTO struct {
	ID            uuid.UUID   `json:"id"`
	UserID        uuid.UUID   `json:"user_id"`
	User          *model.User `json:"user,omitempty"`
	Content       string      `json:"content"`
	Media         []MediaDTO  `json:"media"`
	LikesCount    int64       `json:"likes_count"`
	DislikesCount int64       `json:"dislikes_count"`
	VoteScore     int64       `json:"vote_score"`
	ViewerVote    string      `json:"viewer_vote"`
	CommentsCount int         `json:"comments_count"`
	Liked         bool        `json:"liked"`
	Edited        bool        `json:"edited"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

type MediaDTO struct {
	ID       uuid.UUID `json:"id"`
	URL      string    `json:"url"`
	Position int       `json:"position"`
}
