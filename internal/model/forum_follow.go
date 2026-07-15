package model

import "github.com/google/uuid"

const (
	ForumFollowTargetTopic    = "topic"
	ForumFollowTargetCategory = "category"
	ForumFollowTargetTag      = "tag"
)

type ForumFollow struct {
	Base
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;uniqueIndex:idx_forum_follow_target"`
	TargetType string    `json:"target_type" gorm:"not null;uniqueIndex:idx_forum_follow_target"`
	TargetKey  string    `json:"target_key" gorm:"not null;uniqueIndex:idx_forum_follow_target"`
}

func (ForumFollow) TableName() string { return "forum_follows" }
