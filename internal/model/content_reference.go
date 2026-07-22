package model

import "github.com/google/uuid"

type ContentReference struct {
	Base
	SourceType  string    `json:"source_type" gorm:"type:varchar(32);not null"`
	SourceID    uuid.UUID `json:"source_id" gorm:"type:uuid;not null"`
	SourceField string    `json:"source_field" gorm:"type:varchar(32);not null"`
	TargetType  string    `json:"target_type" gorm:"type:varchar(32);not null"`
	TargetID    uuid.UUID `json:"target_id" gorm:"type:uuid;not null"`
	StartOffset int       `json:"start_offset" gorm:"not null"`
	EndOffset   int       `json:"end_offset" gorm:"not null"`
}

func (ContentReference) TableName() string { return "content_references" }
