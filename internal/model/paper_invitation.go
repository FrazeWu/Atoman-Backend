package model

import (
	"github.com/google/uuid"
	"time"
)

// PaperProjectInvitation is a single-redemption project capability. Only the
// digest of the opaque invitation token is retained; no token in JSON or logs.
type PaperProjectInvitation struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	ProjectID uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	TokenHash string     `gorm:"type:char(64);not null;uniqueIndex" json:"-"`
	Role      string     `gorm:"type:varchar(16);not null" json:"role"`
	ExpiresAt time.Time  `gorm:"not null;index" json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

func (PaperProjectInvitation) TableName() string { return "paper_project_invitations" }
