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

type ReportInput struct {
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

type ModerateInput struct {
	Action   string     `json:"action"`
	ReportID *uuid.UUID `json:"report_id"`
	Reason   string     `json:"reason"`
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
	Items         []CommentDTO     `json:"items"`
	Page          int              `json:"page"`
	PerPage       int              `json:"per_page"`
	TotalRoots    int              `json:"total_roots"`
	TotalComments int              `json:"total_comments"`
	TotalReplies  int              `json:"total_replies"`
	Target        TargetSummaryDTO `json:"target"`
}

type TargetSummaryDTO struct {
	Kind            string     `json:"kind"`
	ResourceID      uuid.UUID  `json:"resource_id"`
	MarkLabel       string     `json:"mark_label"`
	CanMark         bool       `json:"can_mark"`
	MarkedCommentID *uuid.UUID `json:"marked_comment_id,omitempty"`
	CommentCount    int        `json:"comment_count"`
	RootCount       int        `json:"root_count"`
}

type ReplyListDTO struct {
	Items   []CommentDTO `json:"items"`
	Page    int          `json:"page"`
	PerPage int          `json:"per_page"`
	Total   int64        `json:"total"`
	HasMore bool         `json:"has_more"`
}

type PinCommentInput struct {
	CommentID uuid.UUID `json:"comment_id"`
}

type ReportQueueItemDTO struct {
	ID            uuid.UUID  `json:"id"`
	Reason        string     `json:"reason"`
	Note          string     `json:"note"`
	Status        string     `json:"status"`
	ReviewerID    *uuid.UUID `json:"reviewer_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	ReviewedAt    *time.Time `json:"reviewed_at,omitempty"`
	CommentID     uuid.UUID  `json:"comment_id"`
	RootID        uuid.UUID  `json:"root_id"`
	TargetKind    string     `json:"target_kind"`
	ResourceID    uuid.UUID  `json:"resource_id"`
	ReporterID    uuid.UUID  `json:"reporter_id"`
	Username      string     `json:"username"`
	Content       string     `json:"content"`
	CommentStatus string     `json:"comment_status"`
}

type ReportQueueDTO struct {
	Items   []ReportQueueItemDTO `json:"items"`
	Page    int                  `json:"page"`
	PerPage int                  `json:"per_page"`
	Total   int64                `json:"total"`
	HasMore bool                 `json:"has_more"`
}

type CommentResponse struct {
	Data CommentDTO `json:"data"`
}
type CommentListResponse struct {
	Data CommentListDTO `json:"data"`
}
type ReplyListResponse struct {
	Data ReplyListDTO `json:"data"`
}
type ReportQueueResponse struct {
	Data ReportQueueDTO `json:"data"`
}
type ActionResponse struct {
	Data struct {
		OK bool `json:"ok"`
	} `json:"data"`
}
type ErrorBodyDTO struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}
type ErrorResponse struct {
	Error ErrorBodyDTO `json:"error"`
}
