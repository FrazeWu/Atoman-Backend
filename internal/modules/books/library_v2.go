package books

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SaveBookLibraryEntryInput struct {
	EntryID     uuid.UUID `json:"entry_id"`
	BaseVersion int64     `json:"base_version"`
	Status      string    `json:"status"`
	Tags        []string  `json:"tags"`
}

type BookLibraryEntryDTO struct {
	model.BookLibraryEntry
	Tags      []string `json:"tags"`
	WorkTitle string   `json:"work_title"`
}

func normalizeBookPrivateTags(tags []string) ([]string, error) {
	if len(tags) > 32 {
		return nil, apperr.BadRequest("books.invalid_tags", "At most 32 tags are allowed")
	}
	result := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if utf8.RuneCountInString(tag) > 48 {
			return nil, apperr.BadRequest("books.invalid_tags", "Tags must not exceed 48 characters")
		}
		if !seen[tag] {
			seen[tag] = true
			result = append(result, tag)
		}
	}
	sort.Strings(result)
	return result, nil
}

func bookPrivateVersionConflict(code string, version int64, deleted bool) error {
	err := apperr.Conflict(code, "Private data changed; reload before editing")
	err.Details["current_version"] = version
	err.Details["deleted"] = deleted
	return err
}

func bookLibraryEntryDTO(entry model.BookLibraryEntry, title string) (BookLibraryEntryDTO, error) {
	result := BookLibraryEntryDTO{BookLibraryEntry: entry, WorkTitle: title, Tags: []string{}}
	err := json.Unmarshal([]byte(entry.TagsJSON), &result.Tags)
	return result, err
}

func (s *Service) SaveBookLibraryEntry(ctx context.Context, user authctx.CurrentUser, workID uuid.UUID, input SaveBookLibraryEntryInput) (BookLibraryEntryDTO, error) {
	if user.ID == uuid.Nil {
		return BookLibraryEntryDTO{}, apperr.Unauthorized("Login required")
	}
	input.Status = strings.TrimSpace(input.Status)
	if err := validateBookShelfStatus(input.Status); err != nil {
		return BookLibraryEntryDTO{}, err
	}
	if input.BaseVersion < 0 || (input.BaseVersion > 0 && input.EntryID == uuid.Nil) {
		return BookLibraryEntryDTO{}, apperr.BadRequest("books.invalid_library_entry", "Existing entry ID and base version are required for updates")
	}
	tags, err := normalizeBookPrivateTags(input.Tags)
	if err != nil {
		return BookLibraryEntryDTO{}, err
	}
	encoded, _ := json.Marshal(tags)
	var result BookLibraryEntryDTO
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, workID)
		if err != nil {
			return err
		}
		workID = uuid.MustParse(work.ID)
		var entry model.BookLibraryEntry
		load := func() error {
			return tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND work_id = ?", user.ID, workID).First(&entry).Error
		}
		err = load()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if input.BaseVersion != 0 || input.EntryID != uuid.Nil {
				return apperr.NotFound("books.library_entry_not_found", "Library entry not found")
			}
			candidate := model.BookLibraryEntry{UserID: user.ID, WorkID: workID, Status: input.Status, TagsJSON: string(encoded), Version: 1}
			inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
			if inserted.Error != nil {
				return inserted.Error
			}
			if inserted.RowsAffected == 1 {
				result, err = bookLibraryEntryDTO(candidate, work.Title)
				return err
			}
			if err = load(); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		// The stable entry ID prevents an old source-work edit from accidentally
		// changing another entry after a merge redirects that work to a target.
		if input.EntryID != entry.ID && !(input.EntryID == uuid.Nil && input.BaseVersion == 0 && entry.Version == 1 && !entry.DeletedAt.Valid) {
			return bookPrivateVersionConflict("books.library_entry_conflict", entry.Version, entry.DeletedAt.Valid)
		}
		var storedTags []string
		if err := json.Unmarshal([]byte(entry.TagsJSON), &storedTags); err != nil {
			return err
		}
		canonicalTags, _ := json.Marshal(storedTags)
		if !entry.DeletedAt.Valid && entry.Status == input.Status && string(canonicalTags) == string(encoded) && (input.BaseVersion == entry.Version || input.BaseVersion == entry.Version-1) {
			result, err = bookLibraryEntryDTO(entry, work.Title)
			return err
		}
		if input.BaseVersion != entry.Version {
			return bookPrivateVersionConflict("books.library_entry_conflict", entry.Version, entry.DeletedAt.Valid)
		}
		entry.Status = input.Status
		entry.TagsJSON = string(encoded)
		entry.Version++
		entry.DeletedAt = gorm.DeletedAt{}
		if err := tx.Unscoped().Model(&entry).Select("status", "tags_json", "version", "deleted_at", "updated_at").Updates(&entry).Error; err != nil {
			return err
		}
		result, err = bookLibraryEntryDTO(entry, work.Title)
		return err
	})
	return result, err
}

func (s *Service) ListBookLibraryEntries(ctx context.Context, user authctx.CurrentUser, status, tag string, limit, offset int) ([]BookLibraryEntryDTO, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	if status != "" {
		if err := validateBookShelfStatus(status); err != nil {
			return nil, 0, err
		}
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Model(&model.BookLibraryEntry{}).Where("book_library_entries.user_id = ?", user.ID)
	if status != "" {
		query = query.Where("book_library_entries.status = ?", status)
	}
	if tag = strings.ToLower(strings.TrimSpace(tag)); tag != "" {
		query = query.Where("book_library_entries.tags_json @> ?::jsonb", privateTagJSON(tag))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []struct {
		model.BookLibraryEntry
		WorkTitle string
	}
	if err := query.Select("book_library_entries.*, w.title AS work_title").Joins("JOIN book_works w ON w.id = book_library_entries.work_id").Order("book_library_entries.updated_at DESC, book_library_entries.id ASC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	items := make([]BookLibraryEntryDTO, 0, len(rows))
	for _, row := range rows {
		item, err := bookLibraryEntryDTO(row.BookLibraryEntry, row.WorkTitle)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, nil
}

func privateTagJSON(tag string) string {
	encoded, _ := json.Marshal([]string{tag})
	return string(encoded)
}

func (s *Service) DeleteBookLibraryEntry(ctx context.Context, user authctx.CurrentUser, entryID uuid.UUID, baseVersion int64) (BookReadingMarkTombstone, error) {
	if user.ID == uuid.Nil {
		return BookReadingMarkTombstone{}, apperr.Unauthorized("Login required")
	}
	if baseVersion < 1 {
		return BookReadingMarkTombstone{}, apperr.BadRequest("books.invalid_library_entry", "A positive base version is required")
	}
	var result BookReadingMarkTombstone
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var entry model.BookLibraryEntry
		if err := tx.Unscoped().Where("id = ? AND user_id = ?", entryID, user.ID).First(&entry).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("books.library_entry_not_found", "Library entry not found")
			}
			return err
		}
		// Match merge writers' work -> relation lock order, even for a closed work.
		var work model.BookWork
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "SHARE"}).First(&work, "id = ?", entry.WorkID).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", entryID, user.ID).First(&entry).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("books.library_entry_not_found", "Library entry not found")
			}
			return err
		}
		if entry.DeletedAt.Valid {
			if entry.Version != baseVersion+1 {
				return bookPrivateVersionConflict("books.library_entry_conflict", entry.Version, true)
			}
		} else {
			if entry.Version != baseVersion {
				return bookPrivateVersionConflict("books.library_entry_conflict", entry.Version, false)
			}
			entry.Version++
			entry.DeletedAt = privateBookDeletedAt()
			if err := tx.Unscoped().Model(&entry).Select("version", "deleted_at", "updated_at").Updates(&entry).Error; err != nil {
				return err
			}
		}
		result = BookReadingMarkTombstone{ID: entry.ID, Version: entry.Version, DeletedAt: entry.DeletedAt.Time.UTC()}
		return nil
	})
	return result, err
}
