package books

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func catalogCreateHash(kind string, workID uuid.UUID, input any) (string, error) {
	payload, err := json.Marshal(struct {
		Kind   string    `json:"kind"`
		WorkID uuid.UUID `json:"work_id"`
		Input  any       `json:"input"`
	}{Kind: kind, WorkID: workID, Input: input})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

// Calling this within the creation transaction serializes retries with the
// same user + request ID. A UUID reused with different payload is an error.
func catalogCreateReplay(tx *gorm.DB, userID, requestID uuid.UUID, kind, hash string) (uuid.UUID, bool, error) {
	if requestID == uuid.Nil {
		return uuid.Nil, false, nil
	}
	lockKey := "book-catalog-create:" + userID.String() + ":" + requestID.String()
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", lockKey).Error; err != nil {
		return uuid.Nil, false, err
	}
	var receipt model.BookCatalogCreateReceipt
	err := tx.Where("user_id = ? AND request_id = ?", userID, requestID).Take(&receipt).Error
	if err == gorm.ErrRecordNotFound {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	if receipt.Kind != kind || receipt.RequestHash != hash {
		return uuid.Nil, false, apperr.Conflict("books.create_request_reused", "Request ID was previously used with different book creation data")
	}
	return receipt.EntityID, true, nil
}

func saveCatalogCreateReceipt(tx *gorm.DB, userID, requestID uuid.UUID, kind, hash string, entityID uuid.UUID) error {
	if requestID == uuid.Nil {
		return nil
	}
	return tx.Create(&model.BookCatalogCreateReceipt{
		UserID: userID, RequestID: requestID, Kind: kind, RequestHash: hash, EntityID: entityID,
	}).Error
}
