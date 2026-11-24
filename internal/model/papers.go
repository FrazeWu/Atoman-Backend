package model

import (
	"time"

	"github.com/google/uuid"
)

// PaperAccessGrant is an explicit, fail-closed invite to the private Papers module.
type PaperAccessGrant struct {
	UserID    uuid.UUID  `json:"user_id" gorm:"type:uuid;primaryKey"`
	Status    string     `json:"status" gorm:"type:varchar(24);not null;default:'active'"`
	GrantedBy *uuid.UUID `json:"granted_by,omitempty" gorm:"type:uuid"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (PaperAccessGrant) TableName() string { return "paper_access_grants" }

type PaperProject struct {
	Base
	OwnerID      uuid.UUID `json:"-" gorm:"type:uuid;not null;index"`
	Name         string    `json:"name" gorm:"type:varchar(160);not null"`
	MainFileID   uuid.UUID `json:"main_file_id" gorm:"type:uuid"`
	HeadRevision int64     `json:"head_revision" gorm:"not null;default:1"`
}

func (PaperProject) TableName() string { return "paper_projects" }

type PaperFile struct {
	Base
	ProjectID uuid.UUID `json:"-" gorm:"type:uuid;not null;index"`
	Path      string    `json:"path" gorm:"type:varchar(512);not null"`
	Kind      string    `json:"kind" gorm:"type:varchar(24);not null;default:'text'"`
	Content   string    `json:"content" gorm:"type:text;not null"`
	SHA256    string    `json:"sha256" gorm:"type:char(64);not null"`
	Revision  int64     `json:"revision" gorm:"not null;default:1"`
}

func (PaperFile) TableName() string { return "paper_files" }

// PaperFileVersion preserves immutable accepted text versions. AI proposals will
// have their own tables, and must not be inserted here before user acceptance.
type PaperFileVersion struct {
	ID        uuid.UUID `json:"id" gorm:"type:uuid;primaryKey"`
	FileID    uuid.UUID `json:"file_id" gorm:"type:uuid;not null;uniqueIndex:idx_paper_file_versions_file_revision"`
	Revision  int64     `json:"revision" gorm:"not null;uniqueIndex:idx_paper_file_versions_file_revision"`
	Path      string    `json:"path" gorm:"type:varchar(512);not null"`
	Content   string    `json:"content" gorm:"type:text;not null"`
	SHA256    string    `json:"sha256" gorm:"type:char(64);not null"`
	AuthorID  uuid.UUID `json:"-" gorm:"type:uuid;not null"`
	CreatedAt time.Time `json:"created_at"`
}

func (PaperFileVersion) TableName() string { return "paper_file_versions" }
