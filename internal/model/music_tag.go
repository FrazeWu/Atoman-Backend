package model

import "github.com/google/uuid"

const (
	MusicTagKindMood = "mood"
	MusicTagKindType = "type"
)

type MusicTag struct {
	Base
	Name           string    `json:"name" gorm:"type:varchar(48);not null"`
	NormalizedName string    `json:"-" gorm:"type:varchar(48);not null;uniqueIndex:idx_music_tags_kind_name,priority:2"`
	Kind           string    `json:"kind" gorm:"type:varchar(16);not null;uniqueIndex:idx_music_tags_kind_name,priority:1"`
	CreatedBy      uuid.UUID `json:"created_by" gorm:"type:uuid;not null;index"`
	CreatedByUser  *User     `json:"created_by_user,omitempty" gorm:"foreignKey:CreatedBy;references:UUID"`
}

func (MusicTag) TableName() string { return "music_tags" }

type MusicTagAssignment struct {
	Base
	EntityType string    `json:"entity_type" gorm:"type:varchar(16);not null;uniqueIndex:idx_music_tag_assignments_entity_tag,priority:1,where:deleted_at IS NULL"`
	EntityID   uuid.UUID `json:"entity_id" gorm:"type:uuid;not null;uniqueIndex:idx_music_tag_assignments_entity_tag,priority:2,where:deleted_at IS NULL"`
	TagID      uuid.UUID `json:"tag_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_tag_assignments_entity_tag,priority:3,where:deleted_at IS NULL"`
	Tag        *MusicTag `json:"tag,omitempty" gorm:"foreignKey:TagID;references:ID"`
	CreatedBy  uuid.UUID `json:"created_by" gorm:"type:uuid;not null;index"`
}

func (MusicTagAssignment) TableName() string { return "music_tag_assignments" }

type MusicTagVote struct {
	Base
	AssignmentID uuid.UUID `json:"assignment_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_tag_votes_assignment_user,priority:1,where:deleted_at IS NULL"`
	UserID       uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_tag_votes_assignment_user,priority:2,where:deleted_at IS NULL"`
	Vote         string    `json:"vote" gorm:"type:varchar(8);not null;check:chk_music_tag_votes_vote,vote IN ('up','down')"`
}

func (MusicTagVote) TableName() string { return "music_tag_votes" }
