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

// Preview reads the current private sources, not any already published version.
// It never creates or updates a collection or a public snapshot.
type NotePreviewResult struct {
	PublishedNotesContent
	Digest string `json:"digest"`
}
type PreviewNoteCollectionInput struct {
	WorkID uuid.UUID `json:"work_id"`
	NoteCollectionDraft
}

func (s *Service) PreviewNoteCollection(ctx context.Context, user authctx.CurrentUser, input PreviewNoteCollectionInput) (NotePreviewResult, error) {
	out := NotePreviewResult{PublishedNotesContent: PublishedNotesContent{Items: []PublishedNoteItem{}}}
	if user.ID == uuid.Nil {
		return out, apperr.Unauthorized("Login required")
	}
	if input.WorkID == uuid.Nil {
		return out, apperr.BadRequest("books.invalid_work", "A work UUID is required")
	}
	draft, err := normalizeNoteCollectionDraft(input.NoteCollectionDraft)
	if err != nil {
		return out, err
	}
	if len(draft.Items) == 0 {
		return out, apperr.BadRequest("books.note_collection_empty", "Choose at least one note to preview")
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, input.WorkID)
		if err != nil {
			return err
		}
		canonical, err := uuid.Parse(work.ID)
		if err != nil {
			return err
		}
		aliases, err := bookNoteWorkAliases(tx, canonical)
		if err != nil {
			return err
		}
		if err := checkNoteCollectionItems(tx, user.ID, aliases, draft.Items); err != nil {
			return err
		}
		out.PublishedNotesContent, err = snapshotNoteContents(tx, user.ID, aliases, draft)
		return err
	})
	if err != nil {
		return out, err
	}
	encoded, err := json.Marshal(out.PublishedNotesContent)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(encoded)
	out.Digest = hex.EncodeToString(sum[:])
	return out, nil
}

type NoteShareHistoryEntry struct {
	Version     int64     `json:"version"`
	PublishedAt time.Time `json:"published_at"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	NoteCount   int       `json:"note_count"`
	Active      bool      `json:"active"`
}
type NoteShareHistoryPage struct {
	Items []NoteShareHistoryEntry `json:"items"`
	Total int64                   `json:"total"`
}
type NoteShareHistoryDetail struct {
	Version     int64     `json:"version"`
	PublishedAt time.Time `json:"published_at"`
	Active      bool      `json:"active"`
	PublishedNotesContent
}

func (s *Service) ListNoteShareHistory(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, limit, offset int) (NoteShareHistoryPage, error) {
	out := NoteShareHistoryPage{Items: []NoteShareHistoryEntry{}}
	collection, err := ownedNoteCollection(s.db.WithContext(ctx), user, id, false)
	if err != nil {
		return out, err
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	if limit > 100 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Model(&model.BookNoteShareSnapshot{}).Where("collection_id = ?", id)
	if err := q.Count(&out.Total).Error; err != nil {
		return out, err
	}
	var rows []model.BookNoteShareSnapshot
	if err := q.Order("version DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		var content PublishedNotesContent
		if err := json.Unmarshal([]byte(row.ContentJSON), &content); err != nil {
			return out, err
		}
		out.Items = append(out.Items, NoteShareHistoryEntry{Version: row.Version, PublishedAt: row.CreatedAt, Title: content.Title, Description: content.Description, NoteCount: len(content.Items), Active: collection.ActiveSnapshotID != nil && *collection.ActiveSnapshotID == row.ID && collection.Visibility != "private"})
	}
	return out, nil
}
func (s *Service) GetNoteShareHistoryVersion(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, version int64) (NoteShareHistoryDetail, error) {
	out := NoteShareHistoryDetail{}
	if version < 1 {
		return out, apperr.BadRequest("books.invalid_note_version", "Positive publication version required")
	}
	collection, err := ownedNoteCollection(s.db.WithContext(ctx), user, id, false)
	if err != nil {
		return out, err
	}
	var snapshot model.BookNoteShareSnapshot
	err = s.db.WithContext(ctx).Where("collection_id = ? AND version = ?", id, version).First(&snapshot).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, apperr.NotFound("books.note_history_not_found", "Snapshot version not found")
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(snapshot.ContentJSON), &out.PublishedNotesContent); err != nil {
		return out, err
	}
	out.Version = snapshot.Version
	out.PublishedAt = snapshot.CreatedAt
	out.Active = collection.Visibility != "private" && collection.ActiveSnapshotID != nil && *collection.ActiveSnapshotID == snapshot.ID
	return out, nil
}
