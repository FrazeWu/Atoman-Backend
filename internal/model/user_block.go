package model

import "github.com/google/uuid"

type UserBlock struct {
	Base
	BlockerID uuid.UUID `json:"blocker_id" gorm:"type:uuid;not null;index;uniqueIndex:uq_user_block_pair"`
	BlockedID uuid.UUID `json:"blocked_id" gorm:"type:uuid;not null;index;uniqueIndex:uq_user_block_pair"`
	Blocked   *User     `json:"blocked,omitempty" gorm:"foreignKey:BlockedID;references:UUID"`
}

func (UserBlock) TableName() string {
	return "user_blocks"
}
