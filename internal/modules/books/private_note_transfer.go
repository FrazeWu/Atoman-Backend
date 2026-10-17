package books

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BookNoteTransferItem struct {
	ID               uuid.UUID  `json:"id"`
	WorkID           uuid.UUID  `json:"work_id"`
	SourceRevisionID *uuid.UUID `json:"source_revision_id,omitempty"`
	Title            string     `json:"title"`
	Body             string     `json:"body"`
	Tags             []string   `json:"tags"`
}

type BookNoteExport struct {
	SchemaVersion int                    `json:"schema_version"`
	ExportedAt    time.Time              `json:"exported_at"`
	Notes         []BookNoteTransferItem `json:"notes"`
}

type ImportBookPrivateNotesInput struct {
	RequestID     uuid.UUID              `json:"request_id"`
	SchemaVersion int                    `json:"schema_version"`
	Notes         []BookNoteTransferItem `json:"notes"`
}

type BookNoteImportResult struct {
	Created   []uuid.UUID `json:"created"`
	Skipped   []uuid.UUID `json:"skipped"`
	Conflicts []uuid.UUID `json:"conflicts"`
	Duplicate bool        `json:"duplicate"`
}

func (s *Service) ExportBookPrivateNotes(ctx context.Context, user authctx.CurrentUser) (BookNoteExport, error) {
	result := BookNoteExport{SchemaVersion: 1, ExportedAt: time.Now().UTC(), Notes: []BookNoteTransferItem{}}
	if user.ID == uuid.Nil {
		return result, apperr.Unauthorized("Login required")
	}
	if err := s.purgeBookPrivateNotes(ctx, &user.ID, result.ExportedAt); err != nil {
		return result, err
	}
	var notes []model.BookPrivateNote
	if err := s.db.WithContext(ctx).Where("user_id = ?", user.ID).Order("created_at ASC, id ASC").Find(&notes).Error; err != nil {
		return result, err
	}
	for _, note := range notes {
		dto, err := bookPrivateNoteDTO(note)
		if err != nil {
			return result, err
		}
		result.Notes = append(result.Notes, BookNoteTransferItem{ID: note.ID, WorkID: note.WorkID, SourceRevisionID: note.SourceRevisionID, Title: note.Title, Body: note.Body, Tags: dto.Tags})
	}
	return result, nil
}

func (s *Service) ImportBookPrivateNotes(ctx context.Context, user authctx.CurrentUser, input ImportBookPrivateNotesInput) (BookNoteImportResult, error) {
	result := BookNoteImportResult{Created: []uuid.UUID{}, Skipped: []uuid.UUID{}, Conflicts: []uuid.UUID{}}
	if user.ID == uuid.Nil {
		return result, apperr.Unauthorized("Login required")
	}
	if input.RequestID == uuid.Nil || input.SchemaVersion != 1 || len(input.Notes) == 0 || len(input.Notes) > 500 {
		return result, apperr.BadRequest("books.invalid_note_import", "Use schema_version=1, request UUID and 1 to 500 notes per batch")
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 8*1024*1024 {
		return result, apperr.BadRequest("books.invalid_note_import", "Note import must not exceed 8 MiB")
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize imports, not ordinary note editing. Receipt and inserted notes
		// commit together so network retries cannot leave duplicate partial imports.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "book-note-import:"+user.ID.String()).Error; err != nil {
			return err
		}
		var receipt model.BookNoteImportReceipt
		err := tx.Where("user_id = ? AND request_id = ?", user.ID, input.RequestID).First(&receipt).Error
		if err == nil {
			if receipt.RequestHash != hash {
				return apperr.Conflict("books.note_import_request_reused", "Request UUID was already used for different note data")
			}
			if err := json.Unmarshal([]byte(receipt.ResultJSON), &result); err != nil {
				return err
			}
			result.Duplicate = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		service := *s
		service.db = tx
		seen := map[uuid.UUID]bool{}
		for _, item := range input.Notes {
			if item.ID == uuid.Nil || seen[item.ID] {
				return apperr.BadRequest("books.invalid_note_import", "Every imported note needs a distinct source UUID")
			}
			seen[item.ID] = true
			value, err := normalizeBookPrivateNote(SaveBookPrivateNoteInput{WorkID: item.WorkID, SourceRevisionID: item.SourceRevisionID, Title: item.Title, Body: item.Body, Tags: item.Tags})
			if err != nil {
				return err
			}
			// Scope lookup to the current reader. Never read a source account's note.
			var stored model.BookPrivateNote
			err = tx.Unscoped().Where("id = ? AND user_id = ?", item.ID, user.ID).First(&stored).Error
			importID := item.ID
			if errors.Is(err, gorm.ErrRecordNotFound) {
				importID = uuid.NewSHA1(user.ID, []byte("book-note-import:"+item.ID.String()))
				err = tx.Unscoped().Where("id = ? AND user_id = ?", importID, user.ID).First(&stored).Error
			}
			if err == nil {
				existing, err := bookPrivateNoteDTO(stored)
				if err != nil {
					return err
				}
				oldTags, _ := json.Marshal(existing.Tags)
				newTags, _ := json.Marshal(value.Tags)
				// Same exported origin and content is a no-op. Changed or trashed
				// notes are conflicts, never an implicit overwrite or restoration.
				if !stored.DeletedAt.Valid && stored.WorkID == item.WorkID && stored.Title == value.Title && stored.Body == value.Body && sameOptionalBookID(stored.SourceRevisionID, value.SourceRevisionID) && string(oldTags) == string(newTags) {
					result.Skipped = append(result.Skipped, item.ID)
				} else {
					result.Conflicts = append(result.Conflicts, item.ID)
				}
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if _, err := service.SaveBookPrivateNote(ctx, user, importID, value); err != nil {
				return err
			}
			result.Created = append(result.Created, importID)
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Create(&model.BookNoteImportReceipt{UserID: user.ID, RequestID: input.RequestID, RequestHash: hash, ResultJSON: string(payload)}).Error
	})
	if err != nil {
		return BookNoteImportResult{}, err
	}
	return result, nil
}
