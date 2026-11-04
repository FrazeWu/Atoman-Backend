package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// Permanent erase is destructive only for already-trashed private content.
// Retain an anonymized versioned tombstone to block offline resurrection.
func (s *Service) PermanentlyEraseBookPrivateNote(ctx context.Context, user authctx.CurrentUser, noteID uuid.UUID, baseVersion int64) (BookReadingMarkTombstone, error) {
	if user.ID == uuid.Nil {
		return BookReadingMarkTombstone{}, apperr.Unauthorized("Login required")
	}
	if noteID == uuid.Nil || baseVersion < 1 {
		return BookReadingMarkTombstone{}, apperr.BadRequest("books.invalid_private_note", "Note ID and current version required")
	}
	var result BookReadingMarkTombstone
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var note model.BookPrivateNote
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", noteID, user.ID).First(&note).Error; err != nil {
			return privateBookNoteError(err)
		}
		if !note.DeletedAt.Valid {
			return apperr.BadRequest("books.private_note_not_trashed", "Only trashed notes can be permanently erased")
		}
		if note.PurgedAt != nil {
			if note.Version != baseVersion+1 {
				return bookPrivateVersionConflict("books.private_note_conflict", note.Version, true)
			}
		} else {
			if note.Version != baseVersion {
				return bookPrivateVersionConflict("books.private_note_conflict", note.Version, true)
			}
			now := time.Now().UTC()
			note.Version++
			if err := tx.Unscoped().Model(&note).Updates(map[string]any{"title": "", "body": "", "tags_json": "[]", "source_revision_id": nil, "chapter_anchor_json": "{}", "purged_at": now, "version": note.Version}).Error; err != nil {
				return err
			}
		}
		result = BookReadingMarkTombstone{ID: note.ID, Version: note.Version, DeletedAt: note.DeletedAt.Time.UTC()}
		return nil
	})
	return result, err
}
