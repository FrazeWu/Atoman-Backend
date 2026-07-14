package comment

import (
	"time"

	"github.com/google/uuid"
)

const (
	SortOldest = "oldest"
	SortNewest = "newest"
	SortHot    = "hot"
	pageSize   = 20
)

type CreateCommentInput struct {
	Content       string         `json:"content"`
	ReplyToID     *uuid.UUID     `json:"reply_to_id"`
	Mentions      []MentionInput `json:"mentions"`
	AttachmentIDs []uuid.UUID    `json:"attachment_ids"`
}

type EditCommentInput struct {
	Content       string         `json:"content"`
	Mentions      []MentionInput `json:"mentions"`
	AttachmentIDs []uuid.UUID    `json:"attachment_ids"`
}

type ListCommentsInput struct {
	Page int    `form:"page" json:"page"`
	Sort string `form:"sort" json:"sort"`
}

type MentionDTO struct {
	UserID uuid.UUID `json:"user_id"`
	Start  int       `json:"start"`
	End    int       `json:"end"`
}

type AttachmentDTO struct {
	ID          uuid.UUID `json:"id"`
	URL         string    `json:"url"`
	ContentType string    `json:"content_type"`
	Position    int       `json:"position"`
}

type TimeAnchorDTO struct {
	Start   int `json:"start"`
	End     int `json:"end"`
	Seconds int `json:"seconds"`
}

type CommentDTO struct {
	ID           uuid.UUID       `json:"id"`
	AuthorID     uuid.UUID       `json:"author_id"`
	RootID       *uuid.UUID      `json:"root_id,omitempty"`
	ReplyToID    *uuid.UUID      `json:"reply_to_id,omitempty"`
	FloorNumber  *int            `json:"floor_number,omitempty"`
	Content      string          `json:"content"`
	RenderedHTML string          `json:"rendered_html"`
	Status       string          `json:"status"`
	EditedAt     *time.Time      `json:"edited_at,omitempty"`
	LikeCount    int             `json:"like_count"`
	ReplyCount   int             `json:"reply_count"`
	HotScore     float64         `json:"hot_score"`
	CreatedAt    time.Time       `json:"created_at"`
	Marked       bool            `json:"marked"`
	Liked        bool            `json:"liked"`
	Mentions     []MentionDTO    `json:"mentions"`
	Attachments  []AttachmentDTO `json:"attachments"`
	TimeAnchors  []TimeAnchorDTO `json:"time_anchors"`
	Replies      []CommentDTO    `json:"replies"`
}

type CommentListDTO struct {
	Items         []CommentDTO `json:"items"`
	Page          int          `json:"page"`
	PerPage       int          `json:"per_page"`
	TotalRoots    int          `json:"total_roots"`
	TotalComments int          `json:"total_comments"`
	TotalReplies  int          `json:"total_replies"`
}
