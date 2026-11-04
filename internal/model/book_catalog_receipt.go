package model

import (
	"time"

	"github.com/google/uuid"
)

// BookCatalogCreateReceipt makes POST /books/works and POST /books/works/:id/editions
// replay-safe. Only a hash is stored; user-provided metadata is not duplicated.
type BookCatalogCreateReceipt struct {
	UserID      uuid.UUID `gorm:"type:uuid;primaryKey"`
	RequestID   uuid.UUID `gorm:"type:uuid;primaryKey"`
	Kind        string    `gorm:"type:varchar(16);not null"`
	RequestHash string    `gorm:"type:varchar(64);not null"`
	EntityID    uuid.UUID `gorm:"type:uuid;not null"`
	CreatedAt   time.Time
}

func (BookCatalogCreateReceipt) TableName() string { return "book_catalog_create_receipts" }
