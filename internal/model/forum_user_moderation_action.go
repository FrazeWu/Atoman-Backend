package model

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ForumUserModerationAction struct {
	Base
	UserID    uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index:idx_forum_user_moderation_user_time,priority:1"`
	ActorID   uuid.UUID  `json:"actor_id" gorm:"type:uuid;not null;index"`
	Action    string     `json:"action" gorm:"not null"`
	Reason    string     `json:"reason" gorm:"type:text"`
	ExpiresAt *time.Time `json:"expires_at" gorm:"index;index:idx_forum_user_moderation_user_time,priority:2"`
}

func (ForumUserModerationAction) TableName() string { return "forum_user_moderation_actions" }

func (*ForumUserModerationAction) BeforeDelete(*gorm.DB) error {
	return errors.New("forum user moderation actions are append-only")
}

func (*ForumUserModerationAction) BeforeUpdate(*gorm.DB) error {
	return errors.New("forum user moderation actions are append-only")
}
