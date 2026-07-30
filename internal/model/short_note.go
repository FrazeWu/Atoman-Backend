package model

import "github.com/google/uuid"

type ShortNote struct {
	Base
	UserID  uuid.UUID        `json:"user_id" gorm:"type:uuid;not null;index"`
	User    *User            `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Content string           `json:"content" gorm:"type:text;not null"`
	Media   []ShortNoteMedia `json:"media,omitempty" gorm:"foreignKey:ShortNoteID"`
}

func (ShortNote) TableName() string { return "short_notes" }

type ShortNoteMedia struct {
	Base
	ShortNoteID uuid.UUID `json:"short_note_id" gorm:"type:uuid;not null;index"`
	URL         string    `json:"url" gorm:"type:text;not null"`
	Position    int       `json:"position" gorm:"not null"`
}

func (ShortNoteMedia) TableName() string { return "short_note_media" }
