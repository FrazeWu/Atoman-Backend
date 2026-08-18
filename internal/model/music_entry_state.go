package model

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	MusicLifecycleDraft   = "draft"
	MusicLifecycleActive  = "active"
	MusicLifecycleRetired = "retired"
	MusicLifecycleMerged  = "merged"

	MusicEditDevelopment = "development"
	MusicEditLocked      = "locked"
	MusicEditClosed      = "closed"

	MusicStateActionClose  = "close"
	MusicStateActionReopen = "reopen"
	MusicStateActionUnlock = "unlock"

	MusicStateRequestPending    = "pending"
	MusicStateRequestApproved   = "approved"
	MusicStateRequestRejected   = "rejected"
	MusicStateRequestCancelled  = "cancelled"
	MusicStateRequestSuperseded = "superseded"
)

// MusicEntryStateRequest records a user's request to change a Wiki entry's edit state.
type MusicEntryStateRequest struct {
	Base
	EntityType     string     `json:"entity_type" gorm:"type:varchar(16);not null;index;uniqueIndex:idx_music_entry_pending_state_request,priority:1,where:status = 'pending' AND deleted_at IS NULL;check:chk_music_entry_state_request_entity_type,entity_type IN ('artist','album','song')"`
	EntityID       uuid.UUID  `json:"entity_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_entry_pending_state_request,priority:2,where:status = 'pending' AND deleted_at IS NULL"`
	Action         string     `json:"action" gorm:"type:varchar(16);not null;index;check:chk_music_entry_state_request_action,action IN ('close','reopen','unlock')"`
	Status         string     `json:"status" gorm:"type:varchar(16);not null;default:'pending';index;check:chk_music_entry_state_request_status,status IN ('pending','approved','rejected','cancelled','superseded')"`
	BaseRevisionID *uuid.UUID `json:"base_revision_id,omitempty" gorm:"type:uuid;index"`
	BaseRevision   *Revision  `json:"base_revision,omitempty" gorm:"foreignKey:BaseRevisionID;references:ID"`
	RequestedBy    uuid.UUID  `json:"requested_by" gorm:"type:uuid;not null;index"`
	Requester      *User      `json:"requester,omitempty" gorm:"foreignKey:RequestedBy;references:UUID"`
	RequestReason  string     `json:"request_reason" gorm:"type:text;not null"`
	ReviewedBy     *uuid.UUID `json:"reviewed_by,omitempty" gorm:"type:uuid;index"`
	Reviewer       *User      `json:"reviewer,omitempty" gorm:"foreignKey:ReviewedBy;references:UUID"`
	ReviewReason   string     `json:"review_reason" gorm:"type:text"`
	ReviewedAt     *time.Time `json:"reviewed_at,omitempty"`
}

func (MusicEntryStateRequest) TableName() string { return "music_entry_state_requests" }

// MusicEntryStateEvent is the append-only audit trail for automatic, requested, and emergency transitions.
type MusicEntryStateEvent struct {
	Base
	EntityType string                  `json:"entity_type" gorm:"type:varchar(16);not null;index;check:chk_music_entry_state_event_entity_type,entity_type IN ('artist','album','song')"`
	EntityID   uuid.UUID               `json:"entity_id" gorm:"type:uuid;not null;index"`
	FromStatus string                  `json:"from_status" gorm:"type:varchar(16);not null;check:chk_music_entry_state_event_from_status,from_status IN ('development','locked','closed')"`
	ToStatus   string                  `json:"to_status" gorm:"type:varchar(16);not null;check:chk_music_entry_state_event_to_status,to_status IN ('development','locked','closed')"`
	Trigger    string                  `json:"trigger" gorm:"type:varchar(16);not null;index;check:chk_music_entry_state_event_trigger,trigger IN ('request','automatic','emergency')"`
	ActorID    *uuid.UUID              `json:"actor_id,omitempty" gorm:"type:uuid;index"`
	Actor      *User                   `json:"actor,omitempty" gorm:"foreignKey:ActorID;references:UUID"`
	RequestID  *uuid.UUID              `json:"request_id,omitempty" gorm:"type:uuid;index"`
	Request    *MusicEntryStateRequest `json:"request,omitempty" gorm:"foreignKey:RequestID;references:ID"`
	RevisionID *uuid.UUID              `json:"revision_id,omitempty" gorm:"type:uuid;index"`
	Reason     string                  `json:"reason" gorm:"type:text;not null"`
}

func (MusicEntryStateEvent) TableName() string { return "music_entry_state_events" }

func (event *MusicEntryStateEvent) BeforeDelete(_ *gorm.DB) error {
	return errors.New("music entry state events are append-only and cannot be deleted")
}
