package model

import (
	"github.com/google/uuid"
	"time"
)

// Persistent source-to-native migration cursor. The source folder name is
// encrypted with the same user-scoped vault as the account credentials.
type MailExternalImportJob struct {
	ID                 uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID             uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_mail_ext_import_folder;index:idx_mail_ext_import_due"`
	ExternalAccountID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_mail_ext_import_folder"`
	FolderKey          string    `gorm:"type:char(64);not null;uniqueIndex:idx_mail_ext_import_folder"`
	FolderCiphertext   []byte    `gorm:"type:bytea;not null"`
	UIDValidity        uint32    `gorm:"not null"`
	UpperUID           uint32    `gorm:"not null"`
	NextUID            uint32    `gorm:"not null;default:1"`
	Status             string    `gorm:"type:varchar(24);not null;index:idx_mail_ext_import_due"`
	Continuous         bool      `gorm:"not null;default:false"`
	FlagSyncEnabled    bool      `gorm:"not null;default:false"`
	FlagSyncGeneration uint64    `gorm:"not null;default:0"`
	FlagSyncCursorUID  uint32    `gorm:"not null;default:0"`
	FlagLastPolledAt   *time.Time
	FlagLastErrorCode  string `gorm:"type:varchar(48);not null;default:''"`
	LastPolledAt       *time.Time
	Imported           uint32 `gorm:"not null;default:0"`
	Skipped            uint32 `gorm:"not null;default:0"`
	LastErrorCode      string `gorm:"type:varchar(48);not null;default:''"`
	ClaimedAt          *time.Time
	LastCompletedAt    *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (MailExternalImportJob) TableName() string { return "mail_external_import_jobs" }

// Once an Email/import call might have reached the server, its item is
// never sent again without a manually reviewed reconciliation. Message-ID is
// not treated as an identity; source folder+UIDVALIDITY+UID is authoritative.
type MailExternalImportItem struct {
	ID                 uuid.UUID `gorm:"type:uuid;primaryKey"`
	JobID              uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_ext_item_job"`
	UserID             uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_mail_ext_item_identity"`
	ExternalAccountID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_mail_ext_item_identity"`
	FolderKey          string    `gorm:"type:char(64);not null;uniqueIndex:idx_mail_ext_item_identity"`
	UIDValidity        uint32    `gorm:"not null;uniqueIndex:idx_mail_ext_item_identity"`
	SourceUID          uint32    `gorm:"not null;uniqueIndex:idx_mail_ext_item_identity"`
	MIMEHash           string    `gorm:"type:char(64)"`
	Size               uint64
	FlagsBaselineKnown bool       `gorm:"not null;default:false"`
	FlagMutationID     *uuid.UUID `gorm:"type:uuid;index:idx_mail_import_pending_flag"`
	BaselineSeen       bool       `gorm:"not null;default:false"`
	BaselineFlagged    bool       `gorm:"not null;default:false"`
	Status             string     `gorm:"type:varchar(24);not null"`
	ImportedEmailID    string     `gorm:"type:varchar(512)"`
	ErrorCode          string     `gorm:"type:varchar(48)"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (MailExternalImportItem) TableName() string { return "mail_external_import_items" }
