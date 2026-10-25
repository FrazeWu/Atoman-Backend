package shortnote

import (
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

type MediaAttachmentDTO struct {
	Type       string     `json:"type"`
	ID         uuid.UUID  `json:"id"`
	Title      string     `json:"title"`
	Subtitle   string     `json:"subtitle,omitempty"`
	CoverURL   string     `json:"cover_url,omitempty"`
	AudioURL   string     `json:"audio_url,omitempty"`
	Duration   int        `json:"duration,omitempty"`
	Artist     string     `json:"artist,omitempty"`
	AlbumID    *uuid.UUID `json:"album_id,omitempty"`
	AlbumTitle string     `json:"album_title,omitempty"`
	Year       int        `json:"year,omitempty"`
	SongCount  int        `json:"song_count,omitempty"`
}

type noteInput struct {
	Content             string     `json:"content"`
	MediaURLs           []string   `json:"media_urls"`
	MediaAttachmentType *string    `json:"media_attachment_type"`
	MediaAttachmentID   *uuid.UUID `json:"media_attachment_id"`
}

type voteInput struct {
	Direction string `json:"direction"`
}

type NoteDTO struct {
	ID              uuid.UUID            `json:"id"`
	UserID          uuid.UUID            `json:"user_id"`
	User            *model.PublicUserDTO `json:"user,omitempty"`
	Content         string               `json:"content"`
	Media           []MediaDTO           `json:"media"`
	MediaAttachment *MediaAttachmentDTO  `json:"media_attachment,omitempty"`
	LikesCount      int64                `json:"likes_count"`
	DislikesCount   int64                `json:"dislikes_count"`
	VoteScore       int64                `json:"vote_score"`
	ViewerVote      string               `json:"viewer_vote"`
	CommentsCount   int                  `json:"comments_count"`
	Liked           bool                 `json:"liked"`
	Edited          bool                 `json:"edited"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

type MediaDTO struct {
	ID       uuid.UUID `json:"id"`
	URL      string    `json:"url"`
	Position int       `json:"position"`
}
