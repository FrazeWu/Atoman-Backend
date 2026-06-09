package model

import (
	"time"

	"github.com/google/uuid"
)

// Revision represents a version of Album/Song/Artist content
type Revision struct {
	Base
	ContentType string    `json:"content_type" gorm:"not null;index"` // 'album' / 'song' / 'artist'
	ContentID   uuid.UUID `json:"content_id" gorm:"type:uuid;not null;index"`

	// Version control
	VersionNumber      int        `json:"version_number" gorm:"not null"`
	PreviousRevisionID *uuid.UUID `json:"previous_revision_id" gorm:"type:uuid"`
	PreviousRevision   *Revision  `json:"previous_revision,omitempty" gorm:"foreignKey:PreviousRevisionID;references:ID"`

	// Content snapshot (JSON-serialized complete object)
	ContentSnapshot []byte `json:"content_snapshot" gorm:"type:jsonb;not null"`

	// Edit information
	EditorID    uuid.UUID `json:"editor_id" gorm:"type:uuid;not null"`
	Editor      *User     `json:"editor,omitempty" gorm:"foreignKey:EditorID;references:UUID"`
	EditSummary string    `json:"edit_summary" gorm:"type:text"`
	EditType    string    `json:"edit_type" gorm:"default:'edit'"` // 'creation' / 'edit' / 'revert'

	// Status and review
	Status       string     `json:"status" gorm:"default:'pending'"` // 'draft' / 'pending' / 'approved' / 'rejected' / 'superseded'
	ReviewerID   *uuid.UUID `json:"reviewer_id" gorm:"type:uuid"`
	Reviewer     *User      `json:"reviewer,omitempty" gorm:"foreignKey:ReviewerID;references:UUID"`
	ReviewedAt   *time.Time `json:"reviewed_at"`
	ReviewNotes  string     `json:"review_notes" gorm:"type:text"`

	// Metadata
	IsCurrent bool      `json:"is_current" gorm:"default:false;index"` // Whether this is the currently active version
	CreatedAt time.Time `json:"created_at"`
}

func (Revision) TableName() string {
	return "revisions"
}

// EditConflict represents a conflict between two concurrent edits
type EditConflict struct {
	Base
	ContentType string    `json:"content_type" gorm:"not null;index"` // 'album' / 'song'
	ContentID   uuid.UUID `json:"content_id" gorm:"type:uuid;not null;index"`

	// Conflicting revisions
	BaseRevisionID     uuid.UUID `json:"base_revision_id" gorm:"type:uuid;not null"`       // Base revision user was editing from
	ConflictRevisionID uuid.UUID `json:"conflict_revision_id" gorm:"type:uuid;not null"`   // Revision that conflicts
	BaseRevision       *Revision `json:"base_revision,omitempty" gorm:"foreignKey:BaseRevisionID;references:ID"`
	ConflictRevision   *Revision `json:"conflict_revision,omitempty" gorm:"foreignKey:ConflictRevisionID;references:ID"`

	// Conflict details
	FieldName string `json:"field_name" gorm:"not null"` // Which field has conflict
	BaseValue string `json:"base_value" gorm:"type:text"`
	Value1    string `json:"value1" gorm:"type:text"` // User's value
	Value2    string `json:"value2" gorm:"type:text"` // Conflicting value

	// Resolution
	ResolvedValue     *string    `json:"resolved_value" gorm:"type:text"`
	ResolvedBy        *uuid.UUID `json:"resolved_by" gorm:"type:uuid"`
	ResolvedByUser    *User      `json:"resolved_by_user,omitempty" gorm:"foreignKey:ResolvedBy;references:UUID"`
	ResolvedAt        *time.Time `json:"resolved_at"`
	ResolutionType    string     `json:"resolution_type"` // 'auto_merge' / 'manual' / 'keep_mine' / 'take_theirs'

	Status    string    `json:"status" gorm:"default:'unresolved'"` // 'unresolved' / 'resolved'
	CreatedAt time.Time `json:"created_at"`
}

func (EditConflict) TableName() string {
	return "edit_conflicts"
}

// ContentProtection represents protection settings for Albums/Songs
type ContentProtection struct {
	Base
	ContentType string    `json:"content_type" gorm:"not null;index"` // 'album' / 'song'
	ContentID   uuid.UUID `json:"content_id" gorm:"type:uuid;not null;uniqueIndex"`

	ProtectionLevel string `json:"protection_level" gorm:"default:'none'"` // 'none' / 'semi' / 'full'
	// none: anyone can edit
	// semi: edits require approval
	// full: only admin can edit

	ProtectedBy   uuid.UUID  `json:"protected_by" gorm:"type:uuid;not null"` // Admin who set protection
	ProtectedUser *User      `json:"protected_user,omitempty" gorm:"foreignKey:ProtectedBy;references:UUID"`
	Reason        string     `json:"reason" gorm:"type:text"`
	ExpiresAt     *time.Time `json:"expires_at"` // Optional expiration

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (ContentProtection) TableName() string {
	return "content_protections"
}

// Discussion represents discussions on Albums/Songs
type Discussion struct {
	Base
	ContentType string    `json:"content_type" gorm:"not null;index"` // 'album' / 'song'
	ContentID   uuid.UUID `json:"content_id" gorm:"type:uuid;not null;index"`

	UserID uuid.UUID `json:"user_id" gorm:"type:uuid;not null"`
	User   *User     `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`

	Content  string     `json:"content" gorm:"type:text;not null"` // Markdown format
	ParentID *uuid.UUID `json:"parent_id" gorm:"type:uuid"`        // For nested replies
	Parent   *Discussion `json:"parent,omitempty" gorm:"foreignKey:ParentID;references:ID"`
	Replies  []Discussion `json:"replies,omitempty" gorm:"foreignKey:ParentID"`

	Status string `json:"status" gorm:"default:'active'"` // 'active' / 'resolved' / 'deleted'

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ReadAt    *time.Time `json:"read_at" gorm:"index"` // When user read this discussion (NULL = unread)
}

func (Discussion) TableName() string {
	return "discussions"
}
