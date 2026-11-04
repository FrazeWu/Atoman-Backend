package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type NoteShareImpactItem struct {
	CollectionID uuid.UUID `json:"collection_id"`
	Title        string    `json:"title"`
	Visibility   string    `json:"visibility"`
	SharePath    string    `json:"share_path"`
}
type NoteShareImpactResult struct {
	Items []NoteShareImpactItem `json:"items"`
	Total int                   `json:"total"`
}

func activeNoteShareCollections(tx *gorm.DB, owner uuid.UUID, ref BookNoteReference) ([]model.BookNoteCollection, error) {
	encoded, _ := json.Marshal([]BookNoteReference{ref})
	var collections []model.BookNoteCollection
	err := tx.Model(&model.BookNoteCollection{}).Joins("JOIN book_note_share_snapshots s ON s.id = book_note_collections.active_snapshot_id").Where("book_note_collections.user_id = ? AND book_note_collections.visibility IN ? AND s.source_index_json @> ?::jsonb", owner, []string{"link", "public"}, string(encoded)).Order("book_note_collections.id ASC").Find(&collections).Error
	return collections, err
}
func (s *Service) NoteShareImpact(ctx context.Context, user authctx.CurrentUser, ref BookNoteReference) (NoteShareImpactResult, error) {
	out := NoteShareImpactResult{Items: []NoteShareImpactItem{}}
	if user.ID == uuid.Nil {
		return out, apperr.Unauthorized("Login required")
	}
	if ref.ID == uuid.Nil || (ref.Kind != "chapter_note" && ref.Kind != "highlight") {
		return out, apperr.BadRequest("books.invalid_note_reference", "Invalid note reference")
	}
	rows, err := activeNoteShareCollections(s.db.WithContext(ctx), user.ID, ref)
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		var draft NoteCollectionDraft
		if err := json.Unmarshal([]byte(row.DraftJSON), &draft); err != nil {
			return out, err
		}
		out.Items = append(out.Items, NoteShareImpactItem{CollectionID: row.ID, Title: draft.Title, Visibility: row.Visibility, SharePath: "/books/note-shares/" + row.ShareKey})
	}
	out.Total = len(out.Items)
	return out, nil
}

// Must run in the same transaction as the private note/mark soft deletion.
// Revoking the whole stable link avoids silently editing frozen snapshot text.
func revokeNoteSharesForSource(tx *gorm.DB, owner uuid.UUID, ref BookNoteReference) error {
	rows, err := activeNoteShareCollections(tx, owner, ref)
	if err != nil {
		return err
	}
	for _, row := range rows {
		// Re-lock to serialize publication and revocation on this collection.
		var collection model.BookNoteCollection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&collection, "id = ? AND user_id = ?", row.ID, owner).Error; err != nil {
			return err
		}
		if collection.ActiveSnapshotID == nil || collection.Visibility == "private" {
			continue
		}
		var active model.BookNoteShareSnapshot
		if err := tx.First(&active, "id = ? AND collection_id = ?", collection.ActiveSnapshotID, collection.ID).Error; err != nil {
			return err
		}
		var refs []BookNoteReference
		if err := json.Unmarshal([]byte(active.SourceIndexJSON), &refs); err != nil {
			return err
		}
		found := false
		for _, item := range refs {
			if item == ref {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		now := time.Now().UTC()
		collection.Version++
		if err := tx.Model(&collection).Updates(map[string]any{"version": collection.Version, "visibility": "private", "active_snapshot_id": nil, "revoked_at": now}).Error; err != nil {
			return err
		}
	}
	return nil
}
