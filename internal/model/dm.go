package model

import (
	"time"

	"github.com/google/uuid"
)

type DMConversation struct {
	Base
	ParticipantA       uuid.UUID  `json:"participant_a" gorm:"type:uuid;not null;index"`
	ParticipantB       uuid.UUID  `json:"participant_b" gorm:"type:uuid;not null;index"`
	LastMessageAt      *time.Time `json:"last_message_at"`
	LastMessagePreview string     `json:"last_message_preview" gorm:"size:100"`
}

func (DMConversation) TableName() string {
	return "dm_conversations"
}

type DMMessage struct {
	Base
	ConversationID uuid.UUID  `json:"conversation_id" gorm:"type:uuid;not null;index"`
	SenderID       uuid.UUID  `json:"sender_id" gorm:"type:uuid;not null;index"`
	Sender         *User      `json:"sender,omitempty" gorm:"foreignKey:SenderID;references:UUID"`
	Content        string     `json:"content" gorm:"type:text"`
	ImageURL       string     `json:"image_url" gorm:"column:image_url"`
	ReadAt         *time.Time `json:"read_at"`
}

func (DMMessage) TableName() string {
	return "dm_messages"
}
