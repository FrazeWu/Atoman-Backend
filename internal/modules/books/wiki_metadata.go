package books

import (
	"context"
	"encoding/json"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Metadata shares the existing revision/CAS mechanism; files are never edited here.
type PatchBookMetadataInput struct {
	BaseRevisionID uuid.UUID                  `json:"base_revision_id"`
	Summary        string                     `json:"summary"`
	Fields         map[string]json.RawMessage `json:"fields" swaggertype:"object"`
}
type BookWikiMetadata struct {
	EntityType     string          `json:"entity_type"`
	EntityID       uuid.UUID       `json:"entity_id"`
	RevisionID     uuid.UUID       `json:"revision_id"`
	Fields         json.RawMessage `json:"fields" swaggertype:"object"`
	RedirectedFrom *uuid.UUID      `json:"redirected_from,omitempty"`
}

func metadataTable(kind string) (string, error) {
	switch kind {
	case "person":
		return "book_people", nil
	case "edition":
		return "book_editions", nil
	}
	return "", apperr.BadRequest("books.invalid_entity_type", "Expected person or edition")
}

func metadataFields(kind string) []string {
	if kind == "person" {
		return []string{"name", "description"}
	}
	return []string{"title", "subtitle", "edition_statement", "translator_ids", "publisher", "producer", "isbn10", "isbn13", "language", "published_date", "page_count", "binding", "cover_url"}
}

func (s *Service) GetBookWikiMetadata(ctx context.Context, kind string, id uuid.UUID) (BookWikiMetadata, error) {
	table, err := metadataTable(kind)
	if err != nil {
		return BookWikiMetadata{}, err
	}
	result := BookWikiMetadata{EntityType: kind, EntityID: id}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seen := map[uuid.UUID]bool{}
		for {
			if seen[result.EntityID] {
				return apperr.Conflict("books.redirect_cycle", "Invalid metadata redirect")
			}
			seen[result.EntityID] = true
			var row struct {
				LifecycleStatus string
				RedirectTo      *uuid.UUID
				WorkID          uuid.UUID
			}
			if err := tx.Table(table).Where("id = ? AND deleted_at IS NULL", result.EntityID).Take(&row).Error; err != nil {
				return publicBookNotFound(err)
			}
			if row.LifecycleStatus == model.BookLifecycleStatusMerged && row.RedirectTo != nil {
				result.RedirectedFrom = &id
				result.EntityID = *row.RedirectTo
				continue
			}
			if row.LifecycleStatus != model.BookLifecycleStatusActive {
				return apperr.NotFound("books.catalog_not_found", "Entry unavailable")
			}
			if kind == "edition" {
				service := *s
				service.db = tx
				if _, err := service.GetBookWork(ctx, row.WorkID); err != nil {
					return err
				}
			}
			head, err := currentBookWikiRevision(tx, kind, result.EntityID)
			if err != nil {
				return publicBookNotFound(err)
			}
			result.RevisionID = head.ID
			result.Fields = head.ContentSnapshot
			return nil
		}
	})
	return result, err
}

func (s *Service) PatchBookMetadata(ctx context.Context, user authctx.CurrentUser, kind string, id uuid.UUID, input PatchBookMetadataInput, editType string) (BookWikiMetadata, error) {
	if user.ID == uuid.Nil {
		return BookWikiMetadata{}, apperr.Unauthorized("Login required")
	}
	table, err := metadataTable(kind)
	if err != nil {
		return BookWikiMetadata{}, err
	}
	if input.BaseRevisionID == uuid.Nil {
		return BookWikiMetadata{}, apperr.BadRequest("books.base_revision_required", "base_revision_id required")
	}
	allowed := map[string]bool{}
	for _, key := range metadataFields(kind) {
		allowed[key] = true
	}
	for key := range input.Fields {
		if !allowed[key] {
			return BookWikiMetadata{}, apperr.BadRequest("books.invalid_metadata_field", "Unsupported field: "+key)
		}
	}
	var conflict error
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			LifecycleStatus string
			EditStatus      string
		}
		if err := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
			return publicBookNotFound(err)
		}
		if row.LifecycleStatus != model.BookLifecycleStatusActive || row.EditStatus == model.BookEditStatusClosed {
			return apperr.Conflict("books.entry_closed", "Entry is not editable")
		}
		head, err := currentBookWikiRevision(tx, kind, id)
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(head.ContentSnapshot, &fields); err != nil {
			return err
		}
		for key, value := range input.Fields {
			fields[key] = value
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		if head.ID != input.BaseRevisionID {
			if err := recordBookWikiConflict(tx, input.BaseRevisionID, head, encoded); err != nil {
				return err
			}
			conflict = bookRevisionConflict(head)
			return nil
		}
		var snapshot any
		if kind == "person" {
			var person model.BookPerson
			if err := json.Unmarshal(encoded, &person); err != nil {
				return apperr.BadRequest("books.invalid_metadata", "Invalid person fields")
			}
			person.Name = strings.TrimSpace(person.Name)
			person.Description = strings.TrimSpace(person.Description)
			if person.Name == "" {
				return apperr.BadRequest("books.author_required", "Author name required")
			}
			if err := tx.Model(&person).Updates(map[string]any{"name": person.Name, "description": person.Description}).Error; err != nil {
				return err
			}
			snapshot = person
		} else {
			var editionSnapshot bookEditionSnapshot
			if err := json.Unmarshal(encoded, &editionSnapshot); err != nil {
				return apperr.BadRequest("books.invalid_metadata", "Invalid edition fields; date must be an ISO timestamp or null")
			}
			edition := editionSnapshot.BookEdition
			if edition.PageCount < 0 {
				return apperr.BadRequest("books.invalid_metadata", "page_count cannot be negative")
			}
			edition.Publisher = strings.TrimSpace(edition.Publisher)
			edition.Producer = strings.TrimSpace(edition.Producer)
			edition.PublisherID, err = findOrCreateBookPublisher(tx, edition.Publisher)
			if err != nil {
				return err
			}
			edition.ProducerID, err = findOrCreateBookProducer(tx, edition.Producer)
			if err != nil {
				return err
			}
			updates := map[string]any{"title": strings.TrimSpace(edition.Title), "publisher": edition.Publisher, "publisher_id": edition.PublisherID, "producer": edition.Producer, "producer_id": edition.ProducerID, "isbn10": edition.ISBN10, "isbn13": edition.ISBN13, "language": edition.Language, "published_date": edition.PublishedDate, "page_count": edition.PageCount, "binding": edition.Binding, "cover_url": edition.CoverURL}
			updates["subtitle"] = strings.TrimSpace(edition.Subtitle)
			updates["edition_statement"] = strings.TrimSpace(edition.EditionStatement)
			if err := tx.Model(&edition).Updates(updates).Error; err != nil {
				return err
			}
			edition.Title = strings.TrimSpace(edition.Title)
			if _, changed := input.Fields["translator_ids"]; changed {
				ids := editionSnapshot.TranslatorIDs
				if len(ids) > 0 {
					ids, err = resolveBookAuthors(tx, user.ID, ids, nil)
					if err != nil {
						return err
					}
				}
				if err := replaceBookTranslators(tx, edition.ID, ids); err != nil {
					return err
				}
			}
			snapshot, err = loadBookEditionSnapshot(tx, edition)
			if err != nil {
				return err
			}
		}
		_, err = appendBookWikiRevision(tx, kind, id, user.ID, strings.TrimSpace(input.Summary), editType, snapshot)
		return err
	})
	if err != nil {
		return BookWikiMetadata{}, err
	}
	if conflict != nil {
		return BookWikiMetadata{}, conflict
	}
	s.InvalidateCatalog()
	return s.GetBookWikiMetadata(ctx, kind, id)
}

func (s *Service) RevertBookMetadata(ctx context.Context, user authctx.CurrentUser, revision model.Revision, input RevertBookWikiInput) (BookWikiMetadata, error) {
	kind := strings.TrimPrefix(revision.ContentType, "book_")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(revision.ContentSnapshot, &fields); err != nil {
		return BookWikiMetadata{}, err
	}
	selected := map[string]json.RawMessage{}
	for _, key := range metadataFields(kind) {
		if value, exists := fields[key]; exists {
			selected[key] = value
		} else {
			selected[key] = json.RawMessage("null")
		}
	}
	return s.PatchBookMetadata(ctx, user, kind, revision.ContentID, PatchBookMetadataInput{BaseRevisionID: input.BaseRevisionID, Fields: selected, Summary: input.Summary}, "revert")
}
