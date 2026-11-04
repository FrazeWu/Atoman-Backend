package books

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BookNoteReference struct {
	Kind string    `json:"kind"`
	ID   uuid.UUID `json:"id"`
}
type NoteCollectionDraft struct {
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Items       []BookNoteReference `json:"items"`
}
type SaveNoteCollectionInput struct {
	BaseVersion int64     `json:"base_version"`
	WorkID      uuid.UUID `json:"work_id"`
	NoteCollectionDraft
}
type NoteCollectionDTO struct {
	ID               uuid.UUID `json:"id"`
	WorkID           uuid.UUID `json:"work_id"`
	Version          int64     `json:"version"`
	PublishedVersion int64     `json:"published_version"`
	Visibility       string    `json:"visibility"`
	SharePath        string    `json:"share_path,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
	NoteCollectionDraft
}
type NoteCollectionsResult struct {
	Items []NoteCollectionDTO `json:"items"`
	Total int64               `json:"total"`
}

type PublishedNoteItem struct {
	Kind    string `json:"kind"`
	Title   string `json:"title,omitempty"`
	Quote   string `json:"quote,omitempty"`
	Note    string `json:"note,omitempty"`
	Color   string `json:"color,omitempty"`
	Chapter string `json:"chapter,omitempty"`
}
type PublishedNotesContent struct {
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Items       []PublishedNoteItem `json:"items"`
}
type SharedNoteCollectionDTO struct {
	Version     int64     `json:"version"`
	Visibility  string    `json:"visibility"`
	PublishedAt time.Time `json:"published_at"`
	PublishedNotesContent
}

func normalizeNoteCollectionDraft(input NoteCollectionDraft) (NoteCollectionDraft, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 120 || utf8.RuneCountInString(input.Description) > 3000 || len(input.Items) > 40 {
		return input, apperr.BadRequest("books.invalid_note_collection", "A title, description of at most 3000 characters and at most 40 notes are permitted")
	}
	input.Items = append([]BookNoteReference{}, input.Items...)
	seen := map[BookNoteReference]bool{}
	for _, item := range input.Items {
		if item.ID == uuid.Nil || (item.Kind != "chapter_note" && item.Kind != "highlight") || seen[item] {
			return input, apperr.BadRequest("books.invalid_note_collection", "Each item needs a unique private note or highlight UUID")
		}
		seen[item] = true
	}
	return input, nil
}
func newNoteCollectionShareKey() (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secret[:]), nil
}
func noteCollectionDTO(collection model.BookNoteCollection) (NoteCollectionDTO, error) {
	out := NoteCollectionDTO{ID: collection.ID, WorkID: collection.WorkID, Version: collection.Version, PublishedVersion: collection.PublishedVersion, Visibility: collection.Visibility, UpdatedAt: collection.UpdatedAt}
	if collection.ActiveSnapshotID != nil && collection.Visibility != "private" {
		out.SharePath = "/books/note-shares/" + collection.ShareKey
	}
	err := json.Unmarshal([]byte(collection.DraftJSON), &out.NoteCollectionDraft)
	return out, err
}
func ownedNoteCollection(db *gorm.DB, user authctx.CurrentUser, id uuid.UUID, lock bool) (model.BookNoteCollection, error) {
	var collection model.BookNoteCollection
	if user.ID == uuid.Nil {
		return collection, apperr.Unauthorized("Login required")
	}
	query := db.Where("id = ? AND user_id = ?", id, user.ID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&collection).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return collection, apperr.NotFound("books.note_collection_not_found", "Note collection not found")
		}
		return collection, err
	}
	return collection, nil
}

// Validate every selected item on the server; client-supplied quote text is
// never considered authoritative when a public snapshot is created.
func checkNoteCollectionItems(db *gorm.DB, userID uuid.UUID, workIDs []uuid.UUID, items []BookNoteReference) error {
	for _, item := range items {
		var count int64
		var err error
		if item.Kind == "chapter_note" {
			err = db.Model(&model.BookPrivateNote{}).Where("id = ? AND user_id = ? AND work_id IN ? AND purged_at IS NULL", item.ID, userID, workIDs).Count(&count).Error
		} else {
			err = db.Model(&model.BookReadingMark{}).Joins("JOIN book_asset_revisions rev ON rev.id = book_reading_marks.revision_id").Joins("JOIN book_assets a ON a.id = rev.asset_id").Where("book_reading_marks.id = ? AND book_reading_marks.user_id = ? AND book_reading_marks.kind = 'highlight' AND a.work_id IN ?", item.ID, userID, workIDs).Count(&count).Error
		}
		if err != nil {
			return err
		}
		if count != 1 {
			return apperr.BadRequest("books.note_collection_item_unavailable", "Every note must be active, owned by you and belong to the selected work")
		}
	}
	return nil
}
func (s *Service) SaveNoteCollection(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, input SaveNoteCollectionInput) (NoteCollectionDTO, error) {
	if user.ID == uuid.Nil {
		return NoteCollectionDTO{}, apperr.Unauthorized("Login required")
	}
	if id == uuid.Nil || input.WorkID == uuid.Nil || input.BaseVersion < 0 {
		return NoteCollectionDTO{}, apperr.BadRequest("books.invalid_note_collection", "Provide a collection UUID, work ID and base version")
	}
	draft, err := normalizeNoteCollectionDraft(input.NoteCollectionDraft)
	if err != nil {
		return NoteCollectionDTO{}, err
	}
	var result NoteCollectionDTO
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, input.WorkID)
		if err != nil {
			return err
		}
		canonicalID, err := uuid.Parse(work.ID)
		if err != nil {
			return err
		}
		aliases, err := bookNoteWorkAliases(tx, canonicalID)
		if err != nil {
			return err
		}
		if err := checkNoteCollectionItems(tx, user.ID, aliases, draft.Items); err != nil {
			return err
		}
		encoded, err := json.Marshal(draft)
		if err != nil {
			return err
		}
		if input.BaseVersion == 0 {
			key, err := newNoteCollectionShareKey()
			if err != nil {
				return err
			}
			candidate := model.BookNoteCollection{Base: model.Base{ID: id}, UserID: user.ID, WorkID: canonicalID, Version: 1, Visibility: "private", DraftJSON: string(encoded), ShareKey: key}
			attempt := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
			if attempt.Error != nil {
				return attempt.Error
			}
			if attempt.RowsAffected == 1 {
				result, err = noteCollectionDTO(candidate)
				return err
			}
		}
		collection, err := ownedNoteCollection(tx, user, id, true)
		if err != nil {
			return err
		}
		sameWork := false
		for _, alias := range aliases {
			if alias == collection.WorkID {
				sameWork = true
				break
			}
		}
		if !sameWork {
			return apperr.BadRequest("books.note_collection_work_mismatch", "Collection cannot move between unrelated works")
		}
		var old NoteCollectionDraft
		if err := json.Unmarshal([]byte(collection.DraftJSON), &old); err != nil {
			return err
		}
		before, _ := json.Marshal(old)
		if string(before) == string(encoded) && (input.BaseVersion == collection.Version || input.BaseVersion == collection.Version-1) {
			result, err = noteCollectionDTO(collection)
			return err
		}
		if collection.Version != input.BaseVersion {
			return apperr.Conflict("books.note_collection_conflict", "Collection changed; reload before saving")
		}
		collection.Version++
		collection.DraftJSON = string(encoded)
		if err := tx.Model(&collection).Updates(map[string]any{"draft_json": string(encoded), "version": collection.Version}).Error; err != nil {
			return err
		}
		result, err = noteCollectionDTO(collection)
		return err
	})
	return result, err
}
func (s *Service) GetNoteCollection(ctx context.Context, user authctx.CurrentUser, id uuid.UUID) (NoteCollectionDTO, error) {
	item, err := ownedNoteCollection(s.db.WithContext(ctx), user, id, false)
	if err != nil {
		return NoteCollectionDTO{}, err
	}
	return noteCollectionDTO(item)
}
func (s *Service) ListNoteCollections(ctx context.Context, user authctx.CurrentUser, workID uuid.UUID) (NoteCollectionsResult, error) {
	result := NoteCollectionsResult{Items: []NoteCollectionDTO{}}
	if user.ID == uuid.Nil {
		return result, apperr.Unauthorized("Login required")
	}
	q := s.db.WithContext(ctx).Model(&model.BookNoteCollection{}).Where("user_id = ?", user.ID)
	if workID != uuid.Nil {
		work, err := s.GetBookWork(ctx, workID)
		if err != nil {
			return result, err
		}
		canonicalID, err := uuid.Parse(work.ID)
		if err != nil {
			return result, err
		}
		aliases, err := bookNoteWorkAliases(s.db.WithContext(ctx), canonicalID)
		if err != nil {
			return result, err
		}
		q = q.Where("work_id IN ?", aliases)
	}
	if err := q.Count(&result.Total).Error; err != nil {
		return result, err
	}
	var rows []model.BookNoteCollection
	if err := q.Order("updated_at DESC, id ASC").Limit(100).Find(&rows).Error; err != nil {
		return result, err
	}
	for _, row := range rows {
		item, err := noteCollectionDTO(row)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}
