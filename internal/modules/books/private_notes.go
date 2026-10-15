package books

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const bookPrivateNoteTrashRetention = 30 * 24 * time.Hour

type SaveBookPrivateNoteInput struct {
	BaseVersion      int64      `json:"base_version"`
	WorkID           uuid.UUID  `json:"work_id"`
	SourceRevisionID *uuid.UUID `json:"source_revision_id,omitempty"`
	Title            string     `json:"title"`
	Body             string     `json:"body"`
	Tags             []string   `json:"tags"`
}

type BookPrivateNoteDTO struct {
	model.BookPrivateNote
	Tags      []string   `json:"tags"`
	TrashedAt *time.Time `json:"trashed_at,omitempty"`
}

type BookPrivateNoteFilter struct {
	WorkID uuid.UUID
	Tag    string
	Query  string
	Trash  bool
	Limit  int
	Offset int
}

func privateBookDeletedAt() gorm.DeletedAt {
	return gorm.DeletedAt{Time: time.Now().UTC().Truncate(time.Microsecond), Valid: true}
}

func bookPrivateNoteDTO(note model.BookPrivateNote) (BookPrivateNoteDTO, error) {
	result := BookPrivateNoteDTO{BookPrivateNote: note, Tags: []string{}}
	if note.DeletedAt.Valid {
		value := note.DeletedAt.Time.UTC()
		result.TrashedAt = &value
	}
	err := json.Unmarshal([]byte(note.TagsJSON), &result.Tags)
	return result, err
}

func normalizeBookPrivateNote(input SaveBookPrivateNoteInput) (SaveBookPrivateNoteInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	if input.BaseVersion < 0 || input.WorkID == uuid.Nil || len(input.Title) > 255 || len(input.Body) > 200000 || (input.Title == "" && strings.TrimSpace(input.Body) == "") {
		return input, apperr.BadRequest("books.invalid_private_note", "A work and nonempty title or body are required; text exceeds limits or version is invalid")
	}
	if input.SourceRevisionID != nil && *input.SourceRevisionID == uuid.Nil {
		return input, apperr.BadRequest("books.invalid_private_note", "source_revision_id must be a UUID or null")
	}
	tags, err := normalizeBookPrivateTags(input.Tags)
	input.Tags = tags
	return input, err
}

func sameOptionalBookID(a, b *uuid.UUID) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// Resolve the complete alias set without rewriting private note ownership.
func bookNoteWorkAliases(tx *gorm.DB, canonicalID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := tx.Raw(`WITH RECURSIVE aliases(id) AS (
 SELECT id FROM book_works WHERE id = ?
 UNION SELECT w.id FROM book_works w JOIN aliases a ON w.redirect_to = a.id
 WHERE w.lifecycle_status = 'merged' AND w.deleted_at IS NULL
) SELECT id FROM aliases`, canonicalID).Scan(&ids).Error
	return ids, err
}

func (s *Service) SaveBookPrivateNote(ctx context.Context, user authctx.CurrentUser, noteID uuid.UUID, input SaveBookPrivateNoteInput) (BookPrivateNoteDTO, error) {
	if user.ID == uuid.Nil {
		return BookPrivateNoteDTO{}, apperr.Unauthorized("Login required")
	}
	if noteID == uuid.Nil {
		return BookPrivateNoteDTO{}, apperr.BadRequest("books.invalid_private_note", "A client-generated note UUID is required")
	}
	input, err := normalizeBookPrivateNote(input)
	if err != nil {
		return BookPrivateNoteDTO{}, err
	}
	var result BookPrivateNoteDTO
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, input.WorkID)
		if err != nil {
			return err
		}
		canonicalID := uuid.MustParse(work.ID)
		var stored model.BookPrivateNote
		load := func() error {
			return tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", noteID, user.ID).First(&stored).Error
		}
		err = load()
		isNew := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !isNew {
			return err
		}
		if !isNew {
			if stored.DeletedAt.Valid {
				return bookPrivateVersionConflict("books.private_note_conflict", stored.Version, true)
			}
			aliases, err := bookNoteWorkAliases(tx, canonicalID)
			if err != nil {
				return err
			}
			allowed := false
			for _, id := range aliases {
				if id == stored.WorkID {
					allowed = true
					break
				}
			}
			if !allowed {
				return apperr.BadRequest("books.note_work_mismatch", "A note cannot move to another work")
			}
		} else if input.BaseVersion != 0 {
			return apperr.NotFound("books.private_note_not_found", "Private note not found")
		}
		if input.SourceRevisionID != nil && (isNew || !sameOptionalBookID(input.SourceRevisionID, stored.SourceRevisionID)) {
			revision, err := service.readableBookRevision(ctx, *input.SourceRevisionID)
			if err != nil {
				return err
			}
			var asset model.BookAsset
			if err := tx.First(&asset, "id = ? AND work_id = ?", revision.AssetID, canonicalID).Error; err != nil {
				return apperr.BadRequest("books.note_revision_mismatch", "Source revision must belong to this work")
			}
		}
		tags, _ := json.Marshal(input.Tags)
		candidate := model.BookPrivateNote{Base: model.Base{ID: noteID}, UserID: user.ID, WorkID: input.WorkID, SourceRevisionID: input.SourceRevisionID, Title: input.Title, Body: input.Body, TagsJSON: string(tags), Version: 1}
		if isNew {
			inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
			if inserted.Error != nil {
				return inserted.Error
			}
			if inserted.RowsAffected == 1 {
				result, err = bookPrivateNoteDTO(candidate)
				return err
			}
			if err = load(); err != nil {
				return privateBookNoteError(err)
			}
			// A concurrent insert can win the same ID for a different work.
			if stored.WorkID != input.WorkID {
				return bookPrivateVersionConflict("books.private_note_conflict", stored.Version, stored.DeletedAt.Valid)
			}
		}
		if stored.DeletedAt.Valid {
			return bookPrivateVersionConflict("books.private_note_conflict", stored.Version, true)
		}
		old, err := bookPrivateNoteDTO(stored)
		if err != nil {
			return err
		}
		oldTags, _ := json.Marshal(old.Tags)
		identical := stored.Title == input.Title && stored.Body == input.Body && string(oldTags) == string(tags) && sameOptionalBookID(stored.SourceRevisionID, input.SourceRevisionID)
		if identical && (input.BaseVersion == stored.Version || input.BaseVersion == stored.Version-1) {
			result = old
			return nil
		}
		if input.BaseVersion != stored.Version {
			return bookPrivateVersionConflict("books.private_note_conflict", stored.Version, false)
		}
		candidate.Version = stored.Version + 1
		// WorkID is deliberately absent: a merge/undo never relocates private notes.
		if err := tx.Model(&stored).Select("source_revision_id", "title", "body", "tags_json", "version", "updated_at").Updates(&candidate).Error; err != nil {
			return err
		}
		if err = load(); err != nil {
			return err
		}
		result, err = bookPrivateNoteDTO(stored)
		return err
	})
	return result, err
}

func privateBookNoteError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("books.private_note_not_found", "Private note not found")
	}
	return err
}

func (s *Service) GetBookPrivateNote(ctx context.Context, user authctx.CurrentUser, noteID uuid.UUID) (BookPrivateNoteDTO, error) {
	if user.ID == uuid.Nil {
		return BookPrivateNoteDTO{}, apperr.Unauthorized("Login required")
	}
	if err := s.purgeBookPrivateNotes(ctx, &user.ID, time.Now().UTC()); err != nil {
		return BookPrivateNoteDTO{}, err
	}
	var note model.BookPrivateNote
	if err := s.db.WithContext(ctx).Unscoped().Where("id = ? AND user_id = ?", noteID, user.ID).First(&note).Error; err != nil {
		return BookPrivateNoteDTO{}, privateBookNoteError(err)
	}
	if note.PurgedAt != nil || (note.DeletedAt.Valid && !note.DeletedAt.Time.After(time.Now().UTC().Add(-bookPrivateNoteTrashRetention))) {
		return BookPrivateNoteDTO{}, apperr.New(http.StatusGone, "books.private_note_expired", "The 30-day restore period has expired", nil)
	}
	return bookPrivateNoteDTO(note)
}

func (s *Service) ListBookPrivateNotes(ctx context.Context, user authctx.CurrentUser, filter BookPrivateNoteFilter) ([]BookPrivateNoteDTO, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	if err := s.purgeBookPrivateNotes(ctx, &user.ID, time.Now().UTC()); err != nil {
		return nil, 0, err
	}
	items := make([]BookPrivateNoteDTO, 0)
	var total int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&model.BookPrivateNote{}).Where("user_id = ?", user.ID)
		if filter.Trash {
			query = query.Unscoped().Where("deleted_at IS NOT NULL AND deleted_at > ? AND purged_at IS NULL", time.Now().UTC().Add(-bookPrivateNoteTrashRetention))
		}
		if filter.WorkID != uuid.Nil {
			service := *s
			service.db = tx
			work, err := service.GetBookWork(ctx, filter.WorkID)
			if err != nil {
				return err
			}
			aliases, err := bookNoteWorkAliases(tx, uuid.MustParse(work.ID))
			if err != nil {
				return err
			}
			query = query.Where("work_id IN ?", aliases)
		}
		if tag := strings.ToLower(strings.TrimSpace(filter.Tag)); tag != "" {
			query = query.Where("tags_json @> ?::jsonb", privateTagJSON(tag))
		}
		if q := strings.TrimSpace(filter.Query); q != "" {
			query = query.Where("(title ILIKE ? ESCAPE '\\' OR body ILIKE ? ESCAPE '\\')", "%"+escapeBookPrivateSearch(q)+"%", "%"+escapeBookPrivateSearch(q)+"%")
		}
		if err := query.Count(&total).Error; err != nil {
			return err
		}
		var notes []model.BookPrivateNote
		limit, offset := normalizeCatalogPagination(filter.Limit, filter.Offset)
		if err := query.Order("updated_at DESC, id ASC").Limit(limit).Offset(offset).Find(&notes).Error; err != nil {
			return err
		}
		for _, note := range notes {
			item, err := bookPrivateNoteDTO(note)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return nil
	})
	return items, total, err
}

func escapeBookPrivateSearch(value string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(value)
}

func (s *Service) DeleteBookPrivateNote(ctx context.Context, user authctx.CurrentUser, noteID uuid.UUID, baseVersion int64) (BookReadingMarkTombstone, error) {
	if user.ID == uuid.Nil {
		return BookReadingMarkTombstone{}, apperr.Unauthorized("Login required")
	}
	if baseVersion < 1 {
		return BookReadingMarkTombstone{}, apperr.BadRequest("books.invalid_private_note", "A positive base version is required")
	}
	var result BookReadingMarkTombstone
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var note model.BookPrivateNote
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", noteID, user.ID).First(&note).Error; err != nil {
			return privateBookNoteError(err)
		}
		if note.DeletedAt.Valid {
			if note.Version != baseVersion+1 {
				return bookPrivateVersionConflict("books.private_note_conflict", note.Version, true)
			}
		} else {
			if note.Version != baseVersion {
				return bookPrivateVersionConflict("books.private_note_conflict", note.Version, false)
			}
			note.Version++
			note.DeletedAt = privateBookDeletedAt()
			if err := tx.Unscoped().Model(&note).Select("version", "deleted_at", "updated_at").Updates(&note).Error; err != nil {
				return err
			}
		}
		result = BookReadingMarkTombstone{ID: note.ID, Version: note.Version, DeletedAt: note.DeletedAt.Time.UTC()}
		return nil
	})
	return result, err
}

func (s *Service) RestoreBookPrivateNote(ctx context.Context, user authctx.CurrentUser, noteID uuid.UUID, baseVersion int64) (BookPrivateNoteDTO, error) {
	if user.ID == uuid.Nil {
		return BookPrivateNoteDTO{}, apperr.Unauthorized("Login required")
	}
	if baseVersion < 1 {
		return BookPrivateNoteDTO{}, apperr.BadRequest("books.invalid_private_note", "A positive base version is required")
	}
	if err := s.purgeBookPrivateNotes(ctx, &user.ID, time.Now().UTC()); err != nil {
		return BookPrivateNoteDTO{}, err
	}
	var result BookPrivateNoteDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var note model.BookPrivateNote
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", noteID, user.ID).First(&note).Error; err != nil {
			return privateBookNoteError(err)
		}
		if note.PurgedAt != nil || (note.DeletedAt.Valid && !note.DeletedAt.Time.After(time.Now().UTC().Add(-bookPrivateNoteTrashRetention))) {
			return apperr.New(http.StatusGone, "books.private_note_expired", "The 30-day restore period has expired", nil)
		}
		if !note.DeletedAt.Valid {
			if note.Version != baseVersion+1 {
				return bookPrivateVersionConflict("books.private_note_conflict", note.Version, false)
			}
		} else {
			if note.Version != baseVersion {
				return bookPrivateVersionConflict("books.private_note_conflict", note.Version, true)
			}
			note.Version++
			note.DeletedAt = gorm.DeletedAt{}
			if err := tx.Unscoped().Model(&note).Select("version", "deleted_at", "updated_at").Updates(&note).Error; err != nil {
				return err
			}
		}
		var err error
		result, err = bookPrivateNoteDTO(note)
		return err
	})
	return result, err
}

// Erase expired private text but retain the ID tombstone so an old offline PUT
// cannot resurrect it. The daily sweep and reader access both enforce retention.
func (s *Service) purgeBookPrivateNotes(ctx context.Context, userID *uuid.UUID, now time.Time) error {
	query := s.db.WithContext(ctx).Unscoped().Model(&model.BookPrivateNote{}).Where("deleted_at IS NOT NULL AND deleted_at <= ? AND purged_at IS NULL", now.Add(-bookPrivateNoteTrashRetention))
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
	}
	return query.Updates(map[string]any{"title": "", "body": "", "tags_json": "[]", "source_revision_id": nil, "purged_at": now, "version": gorm.Expr("version + 1")}).Error
}
