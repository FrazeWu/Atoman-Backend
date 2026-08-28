package books

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const bookCleanupBatchSize = 32

// ProcessBookCleanup retries object cleanup for imports whose access has already
// been revoked. Object keys are cleared only after every storage operation succeeds.
func (s *Service) ProcessBookCleanup(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("book cleanup database is required")
	}
	if err := requireBookUploadStore(s.bookUpload); err != nil {
		return 0, err
	}
	var sessions []model.UserBookImport
	if err := s.db.WithContext(ctx).Where("status = ? AND object_key <> ''", model.BookImportStatusDeleted).
		Order("updated_at ASC").Limit(bookCleanupBatchSize).Find(&sessions).Error; err != nil {
		return 0, err
	}
	cleaned := 0
	var cleanupErr error
	for _, session := range sessions {
		if err := s.cleanupBookImport(ctx, session.ID); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("cleanup book import %s: %w", session.ID, err))
			continue
		}
		cleaned++
	}
	return cleaned, cleanupErr
}

func (s *Service) cleanupBookImport(ctx context.Context, importID uuid.UUID) error {
	var session model.UserBookImport
	var asset model.UserBookAsset
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", importID).First(&session).Error; err != nil {
			return err
		}
		if session.Status != model.BookImportStatusDeleted || strings.TrimSpace(session.ObjectKey) == "" {
			return nil
		}
		if err := tx.Where("import_id = ?", session.ID).First(&asset).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if session.Status != model.BookImportStatusDeleted || strings.TrimSpace(session.ObjectKey) == "" {
		return nil
	}

	if session.UploadID != "" && session.CompletedAt == nil {
		if err := s.bookUpload.AbortMultipartUpload(session.ObjectKey, session.UploadID); err != nil {
			return err
		}
	}
	keys := []string{session.ObjectKey}
	if asset.ObjectKey != "" && asset.ObjectKey != session.ObjectKey {
		keys = append(keys, asset.ObjectKey)
	}
	if asset.DerivedObjectKey != "" && asset.DerivedObjectKey != session.ObjectKey && asset.DerivedObjectKey != asset.ObjectKey {
		keys = append(keys, asset.DerivedObjectKey)
	}
	for _, key := range keys {
		if err := s.bookUpload.DeleteObject(key); err != nil {
			return err
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.UserBookImport{}).Where("id = ? AND status = ?", importID, model.BookImportStatusDeleted).Updates(map[string]any{
			"object_key": "", "upload_id": "", "completed_parts_json": "[]",
		}).Error; err != nil {
			return err
		}
		if asset.ID != uuid.Nil {
			return tx.Model(&model.UserBookAsset{}).Where("id = ?", asset.ID).Updates(map[string]any{
				"object_key": "", "derived_object_key": "",
			}).Error
		}
		return nil
	})
}
