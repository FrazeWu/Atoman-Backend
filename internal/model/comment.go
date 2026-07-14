package model

import (
	"time"

	"github.com/google/uuid"
)

type DiscussionTarget struct {
	Base
	Kind            string     `json:"kind" gorm:"not null;index"`
	ResourceID      uuid.UUID  `json:"resource_id" gorm:"type:uuid;not null;index"`
	ResourceKey     string     `json:"resource_key" gorm:"type:text;not null"`
	OwnerID         *uuid.UUID `json:"owner_id,omitempty" gorm:"type:uuid;index"`
	CommentCount    int        `json:"comment_count" gorm:"not null;default:0"`
	RootCount       int        `json:"root_count" gorm:"not null;default:0"`
	NextFloor       int        `json:"next_floor" gorm:"not null;default:1"`
	PinnedCommentID *uuid.UUID `json:"pinned_comment_id,omitempty" gorm:"type:uuid"`
}

type CommentEntry struct {
	Base
	TargetID    uuid.UUID  `json:"target_id" gorm:"type:uuid;not null;index"`
	AuthorID    uuid.UUID  `json:"author_id" gorm:"type:uuid;not null;index"`
	RootID      *uuid.UUID `json:"root_id,omitempty" gorm:"type:uuid;index"`
	ReplyToID   *uuid.UUID `json:"reply_to_id,omitempty" gorm:"type:uuid;index"`
	FloorNumber *int       `json:"floor_number,omitempty"`
	Content     string     `json:"content" gorm:"type:text;not null"`
	ContentHash string     `json:"content_hash" gorm:"not null;index"`
	Status      string     `json:"status" gorm:"not null;default:'active';index"`
	EditedAt    *time.Time `json:"edited_at,omitempty"`
	LikeCount   int        `json:"like_count" gorm:"not null;default:0"`
	ReplyCount  int        `json:"reply_count" gorm:"not null;default:0"`
	ReportCount int        `json:"report_count" gorm:"not null;default:0"`
	HotScore    float64    `json:"hot_score" gorm:"not null;default:0;index"`
}

type CommentMention struct {
	Base
	CommentID   uuid.UUID `json:"comment_id" gorm:"type:uuid;not null;index"`
	UserID      uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	StartOffset int       `json:"start_offset" gorm:"not null"`
	EndOffset   int       `json:"end_offset" gorm:"not null"`
}

type CommentAttachment struct {
	Base
	CommentID    uuid.UUID `json:"comment_id" gorm:"type:uuid;not null;index"`
	MediaAssetID uuid.UUID `json:"media_asset_id" gorm:"type:uuid;not null;index"`
	Position     int       `json:"position" gorm:"not null"`
}

type CommentLike struct {
	Base
	CommentID uuid.UUID `json:"comment_id" gorm:"type:uuid;not null;index"`
	UserID    uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
}

type CommentReport struct {
	Base
	CommentID  uuid.UUID  `json:"comment_id" gorm:"type:uuid;not null;index"`
	ReporterID uuid.UUID  `json:"reporter_id" gorm:"type:uuid;not null;index"`
	Reason     string     `json:"reason" gorm:"type:text;not null"`
	Note       string     `json:"note" gorm:"type:text;not null;default:''"`
	Status     string     `json:"status" gorm:"not null;default:'pending';index"`
	ReviewerID *uuid.UUID `json:"reviewer_id,omitempty" gorm:"type:uuid"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
}

type CommentTimeAnchor struct {
	Base
	CommentID   uuid.UUID `json:"comment_id" gorm:"type:uuid;not null;index"`
	StartOffset int       `json:"start_offset" gorm:"not null"`
	EndOffset   int       `json:"end_offset" gorm:"not null"`
	Seconds     int       `json:"seconds" gorm:"not null"`
}
