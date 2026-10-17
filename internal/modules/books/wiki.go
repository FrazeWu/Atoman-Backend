package books

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PatchBookWorkInput struct {
	BaseRevisionID uuid.UUID    `json:"base_revision_id"`
	Title          *string      `json:"title"`
	Subtitle       *string      `json:"subtitle"`
	OriginalTitle  *string      `json:"original_title"`
	Description    *string      `json:"description"`
	Language       *string      `json:"language"`
	AuthorIDs      *[]uuid.UUID `json:"author_ids"`
	Summary        string       `json:"summary"`
}

type RevertBookWikiInput struct {
	BaseRevisionID uuid.UUID `json:"base_revision_id"`
	Summary        string    `json:"summary"`
}

type BookWikiChange struct {
	Field  string          `json:"field"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

type BookWikiRevisionDTO struct {
	ID                 uuid.UUID        `json:"id"`
	EntityType         string           `json:"entity_type"`
	EntityID           uuid.UUID        `json:"entity_id"`
	Version            int              `json:"version"`
	PreviousRevisionID *uuid.UUID       `json:"previous_revision_id"`
	EditorID           uuid.UUID        `json:"editor_id"`
	Summary            string           `json:"summary"`
	EditType           string           `json:"edit_type"`
	IsCurrent          bool             `json:"is_current"`
	Snapshot           json.RawMessage  `json:"snapshot"`
	Changes            []BookWikiChange `json:"changes"`
	CreatedAt          time.Time        `json:"created_at"`
}

func bookWikiDiff(before, after []byte) []BookWikiChange {
	var old, next map[string]json.RawMessage
	_ = json.Unmarshal(before, &old)
	_ = json.Unmarshal(after, &next)
	changes := make([]BookWikiChange, 0)
	// Stable order and only editable metadata, never audit timestamps.
	for _, field := range []string{"title", "subtitle", "original_title", "description", "language", "author_ids", "name", "publisher", "producer", "isbn10", "isbn13", "published_date", "page_count", "binding", "cover_url", "lifecycle_status", "redirect_to"} {
		a, aOK := old[field]
		b, bOK := next[field]
		if !aOK && !bOK {
			continue
		}
		if !aOK {
			a = json.RawMessage("null")
		}
		if !bOK {
			b = json.RawMessage("null")
		}
		if !bytes.Equal(a, b) {
			changes = append(changes, BookWikiChange{Field: field, Before: a, After: b})
		}
	}
	return changes
}

func currentBookWikiRevision(tx *gorm.DB, entityType string, id uuid.UUID) (model.Revision, error) {
	var revision model.Revision
	err := tx.Where("content_type = ? AND content_id = ? AND is_current = true", "book_"+entityType, id).First(&revision).Error
	return revision, err
}

func recordBookWikiConflict(tx *gorm.DB, baseID uuid.UUID, current model.Revision, proposed []byte) error {
	var base model.Revision
	if err := tx.Where("id = ? AND content_type = ? AND content_id = ?", baseID, current.ContentType, current.ContentID).First(&base).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.BadRequest("books.invalid_base_revision", "Base revision does not belong to this entity")
		}
		return err
	}
	var latest map[string]json.RawMessage
	if err := json.Unmarshal(current.ContentSnapshot, &latest); err != nil {
		return err
	}
	changes := bookWikiDiff(base.ContentSnapshot, proposed)
	for _, change := range changes {
		value := latest[change.Field]
		if len(value) == 0 {
			value = json.RawMessage("null")
		}
		conflict := model.EditConflict{ContentType: current.ContentType, ContentID: current.ContentID, BaseRevisionID: base.ID, ConflictRevisionID: current.ID, FieldName: change.Field, BaseValue: string(change.Before), Value1: string(change.After), Value2: string(value), Status: "unresolved"}
		if err := tx.Create(&conflict).Error; err != nil {
			return err
		}
	}
	return nil
}

func bookRevisionConflict(current model.Revision) error {
	err := apperr.Conflict("books.edit_conflict", "The entry changed; reload the current revision before submitting")
	err.Details["current_revision_id"] = current.ID
	return err
}

func (s *Service) PatchBookWork(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, input PatchBookWorkInput) (BookWorkDetail, error) {
	return s.patchBookWork(ctx, user, id, input, "edit")
}

func (s *Service) patchBookWork(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, input PatchBookWorkInput, editType string) (BookWorkDetail, error) {
	if user.ID == uuid.Nil {
		return BookWorkDetail{}, apperr.Unauthorized("Login required")
	}
	if input.BaseRevisionID == uuid.Nil {
		return BookWorkDetail{}, apperr.BadRequest("books.base_revision_required", "base_revision_id is required")
	}
	var conflict error
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var work model.BookWork
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND lifecycle_status = ? AND edit_status <> ?", id, model.BookLifecycleStatusActive, model.BookEditStatusClosed).First(&work).Error; err != nil {
			return publicBookNotFound(err)
		}
		current, err := currentBookWikiRevision(tx, "work", id)
		if err != nil {
			return err
		}
		base := current
		if input.BaseRevisionID != current.ID {
			base = model.Revision{}
			if err := tx.Where("id = ? AND content_type = 'book_work' AND content_id = ?", input.BaseRevisionID, id).First(&base).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return apperr.BadRequest("books.invalid_base_revision", "Base revision does not belong to this work")
				}
				return err
			}
		}
		var snapshot bookWorkSnapshot
		if err := json.Unmarshal(base.ContentSnapshot, &snapshot); err != nil {
			return err
		}
		for _, field := range []struct {
			input  *string
			target *string
		}{
			{input.Title, &snapshot.Title}, {input.Subtitle, &snapshot.Subtitle}, {input.OriginalTitle, &snapshot.OriginalTitle}, {input.Description, &snapshot.Description}, {input.Language, &snapshot.Language},
		} {
			if field.input != nil {
				*field.target = strings.TrimSpace(*field.input)
			}
		}
		if snapshot.Title == "" {
			return apperr.BadRequest("books.title_required", "Book title is required")
		}
		if input.AuthorIDs != nil {
			authors, err := resolveBookAuthors(tx, user.ID, *input.AuthorIDs, nil)
			if err != nil {
				return err
			}
			snapshot.AuthorIDs = authors
		}
		if len(snapshot.AuthorIDs) == 0 {
			return apperr.BadRequest("books.author_required", "At least one author is required")
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		if input.BaseRevisionID != current.ID {
			if err := recordBookWikiConflict(tx, input.BaseRevisionID, current, encoded); err != nil {
				return err
			}
			conflict = bookRevisionConflict(current)
			return nil // Persist conflicts, but never write entry metadata.
		}
		if len(bookWikiDiff(current.ContentSnapshot, encoded)) == 0 {
			return nil
		}
		if err := restoreBookWorkSnapshot(tx, work, snapshot, user.ID); err != nil {
			return err
		}
		_, err = appendBookWikiRevision(tx, "work", id, user.ID, strings.TrimSpace(input.Summary), editType, snapshot)
		return err
	})
	if err != nil {
		return BookWorkDetail{}, err
	}
	if conflict != nil {
		return BookWorkDetail{}, conflict
	}
	s.InvalidateCatalog()
	return s.GetBookWork(ctx, id)
}

func restoreBookWorkSnapshot(tx *gorm.DB, work model.BookWork, snapshot bookWorkSnapshot, userID uuid.UUID) error {
	authors, err := resolveBookAuthors(tx, userID, snapshot.AuthorIDs, nil)
	if err != nil {
		return err
	}
	if err := tx.Model(&work).Updates(map[string]any{"title": snapshot.Title, "subtitle": snapshot.Subtitle, "original_title": snapshot.OriginalTitle, "description": snapshot.Description, "language": snapshot.Language}).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("work_id = ? AND edition_id IS NULL AND role = 'author'", work.ID).Delete(&model.BookContribution{}).Error; err != nil {
		return err
	}
	for i, author := range authors {
		if err := tx.Create(&model.BookContribution{WorkID: &work.ID, PersonID: author, Role: "author", Position: i + 1}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ListBookWikiRevisions(ctx context.Context, entityType string, id uuid.UUID, limit, offset int) ([]BookWikiRevisionDTO, int64, error) {
	var entity any
	switch entityType {
	case "work":
		entity = &model.BookWork{}
	case "edition":
		entity = &model.BookEdition{}
	case "person":
		entity = &model.BookPerson{}
	default:
		return nil, 0, apperr.BadRequest("books.invalid_entity_type", "Unknown book entry type")
	}
	if err := s.db.WithContext(ctx).Where("id = ? AND lifecycle_status IN ?", id, []string{model.BookLifecycleStatusActive, model.BookLifecycleStatusMerged}).First(entity).Error; err != nil {
		return nil, 0, publicBookNotFound(err)
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Model(&model.Revision{}).Where("content_type = ? AND content_id = ? AND status = 'approved'", "book_"+entityType, id)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var revisions []model.Revision
	if err := query.Order("version_number DESC").Offset(offset).Limit(limit).Find(&revisions).Error; err != nil {
		return nil, 0, err
	}
	previousIDs := make([]uuid.UUID, 0, len(revisions))
	for _, revision := range revisions {
		if revision.PreviousRevisionID != nil {
			previousIDs = append(previousIDs, *revision.PreviousRevisionID)
		}
	}
	var previous []model.Revision
	if len(previousIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("id IN ?", previousIDs).Find(&previous).Error; err != nil {
			return nil, 0, err
		}
	}
	snapshots := map[uuid.UUID][]byte{}
	for _, revision := range previous {
		snapshots[revision.ID] = revision.ContentSnapshot
	}
	items := make([]BookWikiRevisionDTO, 0, len(revisions))
	for _, revision := range revisions {
		var before []byte
		if revision.PreviousRevisionID != nil {
			before = snapshots[*revision.PreviousRevisionID]
		}
		items = append(items, BookWikiRevisionDTO{ID: revision.ID, EntityType: entityType, EntityID: id, Version: revision.VersionNumber, PreviousRevisionID: revision.PreviousRevisionID, EditorID: revision.EditorID, Summary: revision.EditSummary, EditType: revision.EditType, IsCurrent: revision.IsCurrent, Snapshot: revision.ContentSnapshot, Changes: bookWikiDiff(before, revision.ContentSnapshot), CreatedAt: revision.CreatedAt})
	}
	return items, total, nil
}

func (s *Service) RevertBookWikiRevision(ctx context.Context, user authctx.CurrentUser, revisionID uuid.UUID, input RevertBookWikiInput) (BookWorkDetail, error) {
	if user.ID == uuid.Nil {
		return BookWorkDetail{}, apperr.Unauthorized("Login required")
	}
	var target model.Revision
	if err := s.db.WithContext(ctx).Where("id = ? AND content_type = 'book_work' AND status = 'approved'", revisionID).First(&target).Error; err != nil {
		return BookWorkDetail{}, publicBookNotFound(err)
	}
	var snapshot bookWorkSnapshot
	if err := json.Unmarshal(target.ContentSnapshot, &snapshot); err != nil {
		return BookWorkDetail{}, err
	}
	// Revert is a new edit, not deletion of subsequent history.
	return s.patchBookWork(ctx, user, target.ContentID, PatchBookWorkInput{BaseRevisionID: input.BaseRevisionID, Title: &snapshot.Title, Subtitle: &snapshot.Subtitle, OriginalTitle: &snapshot.OriginalTitle, Description: &snapshot.Description, Language: &snapshot.Language, AuthorIDs: &snapshot.AuthorIDs, Summary: input.Summary}, "revert")
}
