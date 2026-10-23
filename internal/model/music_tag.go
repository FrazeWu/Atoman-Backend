package model

import (
	"encoding/json"
	"github.com/google/uuid"
)

const (
	MusicTagKindMood       = "mood"
	MusicTagKindType       = "type"
	MusicTagKindScene      = "scene"
	MusicTagKindTheme      = "theme"
	MusicTagKindInstrument = "instrument"
)

type MusicTag struct {
	Base
	Name           string     `json:"name" gorm:"type:varchar(48);not null"`
	NormalizedName string     `json:"-" gorm:"type:varchar(48);not null;uniqueIndex:idx_music_tags_kind_name,priority:2"`
	Kind           string     `json:"kind" gorm:"type:varchar(16);not null;uniqueIndex:idx_music_tags_kind_name,priority:1"`
	ParentID       *uuid.UUID `json:"parent_id,omitempty" gorm:"type:uuid;index"`
	Parent         *MusicTag  `json:"parent,omitempty" gorm:"foreignKey:ParentID;references:ID"`
	Depth          int        `json:"depth" gorm:"not null;default:1"`
	CreatedBy      uuid.UUID  `json:"created_by" gorm:"type:uuid;not null;index"`
	CreatedByUser  *User      `json:"created_by_user,omitempty" gorm:"foreignKey:CreatedBy;references:UUID"`
	Version        int        `json:"version" gorm:"not null;default:1"`
	CanonicalTagID *uuid.UUID `json:"canonical_tag_id,omitempty" gorm:"type:uuid;index"`
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

// 每条修订只记录本次变更涉及的记录，不能用旧的全库快照覆盖后续编辑。
type MusicTagRevision struct {
	Base
	ObjectType string          `json:"object_type" gorm:"not null;uniqueIndex:idx_music_tag_revision_version,priority:1"`
	ObjectID   uuid.UUID       `json:"object_id" gorm:"type:uuid;not null;uniqueIndex:idx_music_tag_revision_version,priority:2"`
	Version    int             `json:"version" gorm:"not null;uniqueIndex:idx_music_tag_revision_version,priority:3"`
	ActorID    uuid.UUID       `json:"actor_id" gorm:"type:uuid;not null;index"`
	Action     string          `json:"action" gorm:"not null"`
	Summary    string          `json:"summary" gorm:"type:text;not null"`
	Before     json.RawMessage `json:"before" gorm:"type:jsonb;not null" swaggertype:"object"`
	After      json.RawMessage `json:"after" gorm:"type:jsonb;not null" swaggertype:"object"`
	RevertsID  *uuid.UUID      `json:"reverts_id,omitempty" gorm:"type:uuid"`
}

func (MusicTagRevision) TableName() string { return "music_tag_revisions" }
