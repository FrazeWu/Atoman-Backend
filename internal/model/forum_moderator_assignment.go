package model

import "github.com/google/uuid"

type ForumModeratorAssignment struct {
	Base
	UserID                   uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	User                     *User          `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	CategoryID               *uuid.UUID     `json:"category_id,omitempty" gorm:"type:uuid;index"`
	Category                 *ForumCategory `json:"category,omitempty" gorm:"foreignKey:CategoryID"`
	CanReviewCategoryRequest bool           `json:"can_review_category_request" gorm:"not null;default:false"`
	CanPinTopic              bool           `json:"can_pin_topic" gorm:"not null;default:false"`
	CanLockTopic             bool           `json:"can_lock_topic" gorm:"not null;default:false"`
}

func (ForumModeratorAssignment) TableName() string { return "forum_moderator_assignments" }
