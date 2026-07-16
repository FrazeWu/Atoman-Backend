package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// StringSlice is a []string that serializes as a JSON text array in PostgreSQL.
type StringSlice []string

// Value implements driver.Valuer — marshals to JSON string for storage.
func (s StringSlice) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements sql.Scanner — unmarshals from JSON string when reading.
func (s *StringSlice) Scan(value interface{}) error {
	if value == nil {
		*s = StringSlice{}
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return fmt.Errorf("StringSlice: unsupported scan type %T", value)
	}
	if len(raw) == 0 || string(raw) == "null" {
		*s = StringSlice{}
		return nil
	}
	return json.Unmarshal(raw, s)
}

// ForumCategory represents a forum category (admin-preset)
type ForumCategory struct {
	Base
	Name        string `json:"name" gorm:"not null;unique"`
	Description string `json:"description" gorm:"type:text"`
	Color       string `json:"color" gorm:"default:'#000000'"` // hex color for UI badge
	TopicCount  int    `json:"topic_count" gorm:"-"`           // computed, not stored
}

func (ForumCategory) TableName() string { return "forum_categories" }

// ForumTopic represents a forum discussion thread
type ForumTopic struct {
	Base
	UserID        uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	User          *User          `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	CategoryID    uuid.UUID      `json:"category_id" gorm:"type:uuid;not null;index"`
	Category      *ForumCategory `json:"category,omitempty" gorm:"foreignKey:CategoryID"`
	Title         string         `json:"title" gorm:"not null"`
	Content       string         `json:"content" gorm:"type:text;not null"` // raw Markdown
	Tags          StringSlice    `json:"tags" gorm:"type:text;default:'[]'"`
	Pinned        bool           `json:"pinned" gorm:"default:false"`
	Featured      bool           `json:"featured" gorm:"default:false"`
	IsSolved      bool           `json:"is_solved" gorm:"-"`
	SolvedReplyID *uuid.UUID     `json:"solved_reply_id" gorm:"-"`
	Closed        bool           `json:"closed" gorm:"default:false"`
	ReplyCount    int            `json:"reply_count" gorm:"-"`
	LikeCount     int            `json:"like_count" gorm:"default:0"`
	ViewCount     int            `json:"view_count" gorm:"default:0"`
	LastReplyAt   *time.Time     `json:"last_reply_at" gorm:"-"`
	IsLiked       bool           `json:"is_liked" gorm:"-"`      // computed per-user
	IsBookmarked  bool           `json:"is_bookmarked" gorm:"-"` // computed per-user
}

func (ForumTopic) TableName() string { return "forum_topics" }

// ForumLike tracks likes on forum topics.
type ForumLike struct {
	Base
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_forum_likes_user_target,priority:1"`
	TargetType string    `json:"target_type" gorm:"not null;uniqueIndex:idx_forum_likes_user_target,priority:2"` // "topic"
	TargetID   uuid.UUID `json:"target_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_forum_likes_user_target,priority:3"`
}

func (ForumLike) TableName() string { return "forum_likes" }

// ForumBookmark tracks topic bookmarks per user
type ForumBookmark struct {
	Base
	UserID  uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_forum_bookmarks_user_topic,priority:1"`
	TopicID uuid.UUID `json:"topic_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_forum_bookmarks_user_topic,priority:2"`
}

func (ForumBookmark) TableName() string { return "forum_bookmarks" }

// ActivityLog records user behaviors for future trust-level algorithm
type ActivityLog struct {
	ID         uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	Action     string    `json:"action" gorm:"not null;index"` // view_topic / create_topic / create_reply / like_post / receive_like / receive_reply
	TargetType string    `json:"target_type"`
	TargetID   uuid.UUID `json:"target_id" gorm:"type:uuid"`
	CreatedAt  time.Time `json:"created_at"`
}

func (ActivityLog) TableName() string { return "activity_logs" }

// ForumDraft persists in-progress topic or reply drafts
type ForumDraft struct {
	Base
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_forum_drafts_user_context,priority:1"`
	ContextKey string    `json:"context_key" gorm:"not null;uniqueIndex:idx_forum_drafts_user_context,priority:2"` // "new_topic" | "reply:{topic_uuid}"
	Title      string    `json:"title"`
	Content    string    `json:"content" gorm:"type:text"`
	Tags       string    `json:"tags"` // comma-separated tag list
}

func (ForumDraft) TableName() string { return "forum_drafts" }

// ForumReport represents a user's report on a topic.
type ForumReport struct {
	Base
	UserID     uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	TargetType string     `json:"target_type" gorm:"not null"` // "topic"
	TargetID   uuid.UUID  `json:"target_id" gorm:"type:uuid;not null;index"`
	Reason     string     `json:"reason" gorm:"not null"` // spam | off-topic | harassment | other
	Note       string     `json:"note" gorm:"type:text"`
	Status     string     `json:"status" gorm:"not null;default:'open';index"`
	ReviewedBy *uuid.UUID `json:"reviewed_by" gorm:"type:uuid"`
	ReviewNote string     `json:"review_note" gorm:"type:text"`
}

func (ForumReport) TableName() string { return "forum_reports" }

// CategoryRequest represents a user's request to create a new forum category.
type CategoryRequest struct {
	Base
	UserID      uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	User        *User      `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Name        string     `json:"name" gorm:"not null"`
	Description string     `json:"description" gorm:"type:text"`
	Reason      string     `json:"reason" gorm:"type:text"`
	Status      string     `json:"status" gorm:"default:'pending'"` // pending | approved | rejected
	ReviewedBy  *uuid.UUID `json:"reviewed_by" gorm:"type:uuid"`
	ReviewNote  string     `json:"review_note" gorm:"type:text"`
}

func (CategoryRequest) TableName() string { return "category_requests" }
