package model

import (
	"time"

	"github.com/google/uuid"
)

const (
	BookResourceStatusActive      = "active"
	BookResourceStatusRemoved     = "removed"
	BookRevisionStatusScanning    = "scanning"
	BookRevisionStatusReady       = "ready"
	BookRevisionStatusFailed      = "failed"
	BookRevisionStatusQuarantined = "quarantined"
	BookRevisionStatusRemoved     = "removed"
)

// BookAsset is a logical public ebook belonging to a publication edition.
// Replacing a file appends a revision; uploading another ebook creates another asset.
type BookAsset struct {
	Base
	// Nil is reserved for legacy files; existing records are not guessed or backfilled.
	EditionID         *uuid.UUID `json:"edition_id,omitempty" gorm:"type:uuid;index"`
	WorkID            uuid.UUID  `json:"work_id" gorm:"type:uuid;not null;index"`
	Language          string     `json:"language" gorm:"not null"`
	Format            string     `json:"format" gorm:"not null;check:chk_book_assets_format,format IN ('epub','pdf','txt','cbz','cbr')"`
	Title             string     `json:"title" gorm:"not null;default:''"`
	Status            string     `json:"status" gorm:"not null;default:'active';check:chk_book_assets_status,status IN ('active','removed')"`
	CurrentRevisionID *uuid.UUID `json:"current_revision_id,omitempty" gorm:"type:uuid"`
	CreatedBy         uuid.UUID  `json:"created_by" gorm:"type:uuid;not null"`
}

func (BookAsset) TableName() string { return "book_assets" }

// BookAssetRevision owns file identity and scan state; MediaAsset only stores
// physical object metadata. Storage keys must never appear in public responses.
type BookAssetRevision struct {
	Base
	AssetID          uuid.UUID `json:"asset_id" gorm:"type:uuid;not null;uniqueIndex:idx_book_asset_revision_number,priority:1"`
	Number           int       `json:"number" gorm:"not null;uniqueIndex:idx_book_asset_revision_number,priority:2;check:chk_book_asset_revision_number,number > 0"`
	MediaAssetID     uuid.UUID `json:"-" gorm:"type:uuid;not null;uniqueIndex"`
	SHA256           string    `json:"sha256" gorm:"type:varchar(64);not null;check:chk_book_asset_revision_hash,sha256 ~ '^[0-9a-f]{64}$'"`
	Status           string    `json:"status" gorm:"not null;default:'scanning';index;check:chk_book_asset_revision_status,status IN ('scanning','ready','failed','quarantined','removed')"`
	OriginalFilename string    `json:"original_filename" gorm:"type:text;not null"`
	CreatedBy        uuid.UUID `json:"created_by" gorm:"type:uuid;not null"`
}

func (BookAssetRevision) TableName() string { return "book_asset_revisions" }

type BookDefaultAsset struct {
	WorkID    uuid.UUID `json:"work_id" gorm:"type:uuid;primaryKey"`
	Language  string    `json:"language" gorm:"primaryKey"`
	AssetID   uuid.UUID `json:"asset_id" gorm:"type:uuid;not null"`
	UpdatedBy uuid.UUID `json:"updated_by" gorm:"type:uuid;not null"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (BookDefaultAsset) TableName() string { return "book_default_assets" }

// A selection pins a revision, so a community default or replacement cannot
// silently move an existing reader to a different file.
type BookReadingSelection struct {
	UserID     uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	WorkID     uuid.UUID `json:"work_id" gorm:"type:uuid;primaryKey"`
	AssetID    uuid.UUID `json:"asset_id" gorm:"type:uuid;not null"`
	RevisionID uuid.UUID `json:"revision_id" gorm:"type:uuid;not null"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (BookReadingSelection) TableName() string { return "book_reading_selections" }

type BookReadingState struct {
	UserID          uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	RevisionID      uuid.UUID `json:"revision_id" gorm:"type:uuid;primaryKey"`
	EPUBCFI         string    `json:"epub_cfi" gorm:"type:text;not null;default:''"`
	PDFPage         int       `json:"pdf_page" gorm:"not null;default:0;check:chk_book_reading_page,pdf_page >= 0"`
	TXTOffset       int64     `json:"txt_offset" gorm:"not null;default:0;check:chk_book_reading_offset,txt_offset >= 0"`
	CurrentPercent  float64   `json:"current_percent" gorm:"not null;default:0;check:chk_book_reading_current,current_percent >= 0 AND current_percent <= 1"`
	FurthestPercent float64   `json:"furthest_percent" gorm:"not null;default:0;check:chk_book_reading_furthest,furthest_percent >= current_percent AND furthest_percent <= 1"`
	Version         int64     `json:"version" gorm:"not null;default:0"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (BookReadingState) TableName() string { return "book_reading_states" }
