package books

import (
	"context"
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

type BookListEntry struct {
	WorkID         uuid.UUID `json:"work_id"`
	Recommendation string    `json:"recommendation"`
}

type BookListSnapshot struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Items       []BookListEntry `json:"items"`
}

type SaveBookListDraftInput struct {
	BaseVersion int64 `json:"base_version"`
	BookListSnapshot
}

type PublishBookListInput struct {
	BaseVersion int64  `json:"base_version"`
	Visibility  string `json:"visibility"`
}

type BookListVersionInput struct {
	BaseVersion int64 `json:"base_version"`
}

type BookListDraftDTO struct {
	ID               uuid.UUID  `json:"id"`
	Version          int64      `json:"version"`
	PublishedVersion int64      `json:"published_version"`
	Visibility       string     `json:"visibility"`
	PublishedAt      *time.Time `json:"published_at,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
	BookListSnapshot
}

type BookListChanges struct {
	ItemsAdded             int  `json:"items_added"`
	ItemsRemoved           int  `json:"items_removed"`
	RecommendationsChanged int  `json:"recommendations_changed"`
	OrderChanged           bool `json:"order_changed"`
	MetadataChanged        bool `json:"metadata_changed"`
}

type bookListWork struct {
	WorkID          uuid.UUID `gorm:"column:work_id"`
	CanonicalWorkID uuid.UUID `gorm:"column:canonical_work_id"`
	Title           string
}

// Resolve every original ID in one query. Lists retain the original IDs and
// recommendation order across a work merge/undo; unavailable works are not lost.
func resolveBookListWorks(db *gorm.DB, items []BookListEntry) (map[uuid.UUID]bookListWork, error) {
	result := map[uuid.UUID]bookListWork{}
	if len(items) == 0 {
		return result, nil
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.WorkID)
	}
	var rows []bookListWork
	err := db.Raw(`WITH RECURSIVE resolved AS (
 SELECT w.id AS original_id, w.id, w.redirect_to, w.lifecycle_status, w.edit_status, w.title, ARRAY[w.id] AS path
 FROM book_works w WHERE w.id IN ? AND w.deleted_at IS NULL
 UNION ALL
 SELECT r.original_id, w.id, w.redirect_to, w.lifecycle_status, w.edit_status, w.title, r.path || w.id
 FROM resolved r JOIN book_works w ON w.id = r.redirect_to AND w.deleted_at IS NULL
 WHERE r.lifecycle_status = 'merged' AND NOT w.id = ANY(r.path)
) SELECT original_id AS work_id, id AS canonical_work_id, title FROM resolved r
WHERE lifecycle_status = 'active' AND edit_status <> 'closed'
AND EXISTS (SELECT 1 FROM revisions WHERE content_type = 'book_work' AND content_id = r.id AND is_current = true AND deleted_at IS NULL)`, ids).Scan(&rows).Error
	for _, row := range rows {
		result[row.WorkID] = row
	}
	return result, err
}

func normalizeBookListSnapshot(db *gorm.DB, snapshot BookListSnapshot) (BookListSnapshot, error) {
	snapshot.Title = strings.TrimSpace(snapshot.Title)
	snapshot.Description = strings.TrimSpace(snapshot.Description)
	if snapshot.Title == "" || utf8.RuneCountInString(snapshot.Title) > 120 || utf8.RuneCountInString(snapshot.Description) > 10000 || len(snapshot.Items) > 200 {
		return snapshot, apperr.BadRequest("books.invalid_list", "A title (up to 120 characters), description (up to 10000) and at most 200 books are allowed")
	}
	snapshot.Items = append([]BookListEntry{}, snapshot.Items...)
	for i := range snapshot.Items {
		item := &snapshot.Items[i]
		item.Recommendation = strings.TrimSpace(item.Recommendation)
		if item.WorkID == uuid.Nil || utf8.RuneCountInString(item.Recommendation) > 2000 {
			return snapshot, apperr.BadRequest("books.invalid_list_entry", "A work UUID and a recommendation up to 2000 characters are required")
		}
	}
	works, err := resolveBookListWorks(db, snapshot.Items)
	if err != nil {
		return snapshot, err
	}
	seen := map[uuid.UUID]bool{}
	for _, item := range snapshot.Items {
		work, exists := works[item.WorkID]
		if !exists {
			return snapshot, apperr.BadRequest("books.list_work_unavailable", "Every list item must reference an available new catalog work")
		}
		if seen[work.CanonicalWorkID] {
			return snapshot, apperr.BadRequest("books.duplicate_list_work", "A work and its merge aliases cannot appear more than once")
		}
		seen[work.CanonicalWorkID] = true
	}
	return snapshot, nil
}

func bookListDraftDTO(list model.BookList) (BookListDraftDTO, error) {
	result := BookListDraftDTO{ID: list.ID, Version: list.Version, PublishedVersion: list.PublishedVersion, Visibility: list.Visibility, PublishedAt: list.PublishedAt, UpdatedAt: list.UpdatedAt}
	err := json.Unmarshal([]byte(list.DraftJSON), &result.BookListSnapshot)
	return result, err
}

func ownedBookList(db *gorm.DB, user authctx.CurrentUser, id uuid.UUID, lock bool) (model.BookList, error) {
	var list model.BookList
	if user.ID == uuid.Nil {
		return list, apperr.Unauthorized("Login required")
	}
	query := db.Unscoped().Where("id = ? AND user_id = ?", id, user.ID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.First(&list).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && list.DeletedAt.Valid) {
		return list, apperr.NotFound("books.list_not_found", "Book list not found")
	}
	return list, err
}

func bookListVersionConflict(list model.BookList) error {
	return bookPrivateVersionConflict("books.list_version_conflict", list.Version, list.DeletedAt.Valid)
}

func (s *Service) SaveBookListDraft(ctx context.Context, user authctx.CurrentUser, listID uuid.UUID, input SaveBookListDraftInput) (BookListDraftDTO, error) {
	if user.ID == uuid.Nil {
		return BookListDraftDTO{}, apperr.Unauthorized("Login required")
	}
	if listID == uuid.Nil || input.BaseVersion < 0 {
		return BookListDraftDTO{}, apperr.BadRequest("books.invalid_list", "A client-generated list UUID and nonnegative base version are required")
	}
	var result BookListDraftDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot, err := normalizeBookListSnapshot(tx, input.BookListSnapshot)
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(snapshot)
		if input.BaseVersion == 0 {
			candidate := model.BookList{Base: model.Base{ID: listID}, UserID: user.ID, Version: 1, DraftJSON: string(encoded), Visibility: "private"}
			created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
			if created.Error != nil {
				return created.Error
			}
			if created.RowsAffected == 1 {
				result, err = bookListDraftDTO(candidate)
				return err
			}
		}
		list, err := ownedBookList(tx, user, listID, true)
		if err != nil {
			return err
		}
		// jsonb storage reorders object keys; compare normalized JSON values.
		var previous BookListSnapshot
		if err := json.Unmarshal([]byte(list.DraftJSON), &previous); err != nil {
			return err
		}
		before, _ := json.Marshal(previous)
		if string(before) == string(encoded) && (input.BaseVersion == list.Version || input.BaseVersion == list.Version-1) {
			result, err = bookListDraftDTO(list)
			return err
		}
		if list.Version != input.BaseVersion {
			return bookListVersionConflict(list)
		}
		if err := tx.Model(&list).Updates(map[string]any{"draft_json": string(encoded), "version": list.Version + 1}).Error; err != nil {
			return err
		}
		result, err = bookListDraftDTO(list)
		return err
	})
	return result, err
}

func (s *Service) GetBookListDraft(ctx context.Context, user authctx.CurrentUser, listID uuid.UUID) (BookListDraftDTO, error) {
	list, err := ownedBookList(s.db.WithContext(ctx), user, listID, false)
	if err != nil {
		return BookListDraftDTO{}, err
	}
	return bookListDraftDTO(list)
}

func describeBookListChanges(old, next BookListSnapshot) BookListChanges {
	changes := BookListChanges{MetadataChanged: old.Title != next.Title || old.Description != next.Description}
	previous := map[uuid.UUID]int{}
	for i, item := range old.Items {
		previous[item.WorkID] = i
	}
	current := map[uuid.UUID]bool{}
	for i, item := range next.Items {
		current[item.WorkID] = true
		if index, exists := previous[item.WorkID]; exists {
			if old.Items[index].Recommendation != item.Recommendation {
				changes.RecommendationsChanged++
			}
			changes.OrderChanged = changes.OrderChanged || index != i
		} else {
			changes.ItemsAdded++
		}
	}
	for _, item := range old.Items {
		if !current[item.WorkID] {
			changes.ItemsRemoved++
		}
	}
	return changes
}

func (s *Service) PublishBookList(ctx context.Context, user authctx.CurrentUser, listID uuid.UUID, input PublishBookListInput) (BookListDraftDTO, error) {
	if input.BaseVersion < 1 || (input.Visibility != "public" && input.Visibility != "private") {
		return BookListDraftDTO{}, apperr.BadRequest("books.invalid_list_publication", "Provide the reviewed draft version and public/private visibility")
	}
	var result BookListDraftDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		list, err := ownedBookList(tx, user, listID, true)
		if err != nil {
			return err
		}
		// Compare jsonb in SQL so equivalent stored snapshots have stable retries.
		var identical bool
		if err := tx.Raw("SELECT draft_json = published_json FROM book_lists WHERE id = ?", list.ID).Scan(&identical).Error; err != nil {
			return err
		}
		if identical && list.PublishedAt != nil && list.Visibility == input.Visibility && (input.BaseVersion == list.Version || input.BaseVersion == list.Version-1) {
			result, err = bookListDraftDTO(list)
			return err
		}
		if list.Version != input.BaseVersion {
			return bookListVersionConflict(list)
		}
		var snapshot BookListSnapshot
		if err := json.Unmarshal([]byte(list.DraftJSON), &snapshot); err != nil {
			return err
		}
		if len(snapshot.Items) == 0 {
			return apperr.BadRequest("books.empty_list", "Add at least one book before publishing")
		}
		if _, err := normalizeBookListSnapshot(tx, snapshot); err != nil {
			return err
		}
		var previous BookListSnapshot
		// Do not expose a diff against a previously private publication.
		if list.Visibility == "public" && list.PublishedAt != nil {
			if err := json.Unmarshal([]byte(list.PublishedJSON), &previous); err != nil {
				return err
			}
		}
		changes, _ := json.Marshal(describeBookListChanges(previous, snapshot))
		now := time.Now().UTC().Truncate(time.Microsecond)
		if err := tx.Model(&list).Updates(map[string]any{"published_json": list.DraftJSON, "visibility": input.Visibility,
			"published_version": list.PublishedVersion + 1, "published_at": now, "changes_json": string(changes), "version": list.Version + 1}).Error; err != nil {
			return err
		}
		result, err = bookListDraftDTO(list)
		return err
	})
	return result, err
}

func (s *Service) WithdrawBookList(ctx context.Context, user authctx.CurrentUser, listID uuid.UUID, baseVersion int64, remove bool) error {
	if baseVersion < 1 {
		return apperr.BadRequest("books.invalid_list", "A positive base version is required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		list, err := ownedBookList(tx, user, listID, true)
		if err != nil {
			return err
		}
		if list.Version != baseVersion {
			return bookListVersionConflict(list)
		}
		updates := map[string]any{"visibility": "private", "version": list.Version + 1}
		if remove {
			updates["deleted_at"] = time.Now().UTC()
		}
		return tx.Model(&list).Updates(updates).Error
	})
}
