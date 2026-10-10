package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const bookEditPayloadMaxSize = 64 * 1024

type BookEditSourceInput struct {
	URL   string `json:"url"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

type SubmitBookEditInput struct {
	Type       string                `json:"type"`
	EntityType string                `json:"entity_type"`
	EntityID   string                `json:"entity_id"`
	Payload    map[string]any        `json:"payload"`
	Reason     string                `json:"reason"`
	Sources    []BookEditSourceInput `json:"sources"`
}

type BookEditDTO struct {
	ID            string                `json:"id"`
	Type          string                `json:"type"`
	EntityType    string                `json:"entity_type"`
	EntityID      string                `json:"entity_id,omitempty"`
	Status        string                `json:"status"`
	Reason        string                `json:"reason,omitempty"`
	DecisionNote  string                `json:"decision_note,omitempty"`
	CreatedAt     time.Time             `json:"created_at"`
	ReviewedAt    *time.Time            `json:"reviewed_at,omitempty"`
	Sources       []BookPublicSourceDTO `json:"sources"`
	UpvoteCount   int64                 `json:"upvote_count"`
	DownvoteCount int64                 `json:"downvote_count"`
	Payload       map[string]any        `json:"payload"`
}

func (s *Service) SubmitBookEdit(user authctx.CurrentUser, input SubmitBookEditInput) (BookEditDTO, error) {
	if user.ID == uuid.Nil {
		return BookEditDTO{}, apperr.Unauthorized("Login required")
	}
	if err := validateBookEditInput(input); err != nil {
		return BookEditDTO{}, err
	}
	var entityID *uuid.UUID
	if strings.TrimSpace(input.EntityID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(input.EntityID))
		if err != nil || parsed == uuid.Nil {
			return BookEditDTO{}, apperr.BadRequest("validation.invalid_request", "entity_id must be a valid UUID")
		}
		entityID = &parsed
	}
	if input.Type != model.BookEditTypeCreate && entityID == nil {
		return BookEditDTO{}, apperr.BadRequest("validation.invalid_request", "entity_id is required for this edit")
	}
	payload := input.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > bookEditPayloadMaxSize {
		return BookEditDTO{}, apperr.BadRequest("validation.invalid_request", "edit payload is invalid")
	}
	if err := s.validateBookEditTarget(input.Type, input.EntityType, entityID, payload); err != nil {
		return BookEditDTO{}, err
	}
	if err := validateBookRelations(s.db, input.EntityType, payload); err != nil {
		return BookEditDTO{}, err
	}
	return s.createBookEdit(user, input, entityID, string(encoded))
}

func (s *Service) createBookEdit(user authctx.CurrentUser, input SubmitBookEditInput, entityID *uuid.UUID, payload string) (BookEditDTO, error) {
	var edit model.BookEdit
	err := s.db.Transaction(func(tx *gorm.DB) error {
		edit = model.BookEdit{
			Type: input.Type, EntityType: input.EntityType, EntityID: entityID, SubmittedBy: user.ID,
			Status: model.BookEditStatusPending, PayloadJSON: payload, ChangesJSON: payload, Reason: strings.TrimSpace(input.Reason),
		}
		if err := tx.Create(&edit).Error; err != nil {
			return err
		}
		targetID := edit.ID
		if entityID != nil {
			targetID = *entityID
		}
		for _, source := range input.Sources {
			row := model.BookSource{TargetType: input.EntityType, TargetID: targetID, BookEditID: &edit.ID, Kind: strings.TrimSpace(source.Kind), Title: strings.TrimSpace(source.Title), URL: strings.TrimSpace(source.URL), Note: strings.TrimSpace(source.Note), SubmittedBy: &user.ID}
			if row.Kind == "" {
				row.Kind = "bibliographic"
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return BookEditDTO{}, err
	}
	s.InvalidateCatalog()
	return buildBookEditDTO(s.db, edit), nil
}

func (s *Service) ListBookEdits(ctx context.Context, user authctx.CurrentUser, reviewQueue bool, limit, offset int) ([]BookEditDTO, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	if reviewQueue && !CanReviewSubmission(BookViewer{UserID: user.ID, Role: user.Role}, uuid.New()) {
		return nil, 0, apperr.Forbidden("books.review_forbidden", "Book review permission is required")
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Model(&model.BookEdit{})
	if reviewQueue {
		query = query.Where("status = ? AND submitted_by <> ?", model.BookEditStatusPending, user.ID)
	} else {
		query = query.Where("submitted_by = ?", user.ID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var edits []model.BookEdit
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&edits).Error; err != nil {
		return nil, 0, err
	}
	result := make([]BookEditDTO, 0, len(edits))
	for _, edit := range edits {
		result = append(result, buildBookEditDTO(s.db, edit))
	}
	return result, total, nil
}

func (s *Service) WithdrawBookEdit(user authctx.CurrentUser, editID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	result := s.db.Model(&model.BookEdit{}).Where("id = ? AND submitted_by = ? AND status = ?", editID, user.ID, model.BookEditStatusPending).Updates(map[string]any{"status": model.BookEditStatusWithdrawn})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperr.NotFound("books.edit_not_found", "Book edit not found or cannot be withdrawn")
	}
	return nil
}

func (s *Service) ReviewBookEdit(reviewer authctx.CurrentUser, editID uuid.UUID, decision, note string) (BookEditDTO, error) {
	if reviewer.ID == uuid.Nil {
		return BookEditDTO{}, apperr.Unauthorized("Login required")
	}
	if !CanReviewSubmission(BookViewer{UserID: reviewer.ID, Role: reviewer.Role}, uuid.New()) {
		return BookEditDTO{}, apperr.Forbidden("books.review_forbidden", "Book review permission is required")
	}
	if decision != model.BookEditStatusApproved && decision != model.BookEditStatusRejected {
		return BookEditDTO{}, apperr.BadRequest("validation.invalid_request", "review decision is invalid")
	}
	var edit model.BookEdit
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", editID).First(&edit).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("books.edit_not_found", "Book edit not found")
			}
			return err
		}
		if edit.SubmittedBy == reviewer.ID {
			return apperr.Forbidden("books.self_review_forbidden", "You cannot review your own book edit")
		}
		if edit.Status != model.BookEditStatusPending {
			return apperr.Conflict("books.edit_already_reviewed", "Book edit is no longer pending")
		}
		if decision == model.BookEditStatusApproved {
			if err := s.applyBookEdit(tx, &edit); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		edit.Status = decision
		edit.ReviewerID = &reviewer.ID
		edit.ReviewedAt = &now
		edit.DecisionNote = strings.TrimSpace(note)
		if err := tx.Save(&edit).Error; err != nil {
			return err
		}
		return audit.Record(tx, audit.Entry{ActorID: &reviewer.ID, Action: "books.edit." + decision, EntityType: "book_edit", EntityID: &edit.ID, Reason: edit.DecisionNote})
	})
	if err != nil {
		return BookEditDTO{}, err
	}
	s.InvalidateCatalog()
	return buildBookEditDTO(s.db, edit), nil
}

func (s *Service) validateBookEditTarget(editType, entityType string, entityID *uuid.UUID, payload map[string]any) error {
	if editType == model.BookEditTypeCreate || entityID == nil {
		return nil
	}
	if editType == model.BookEditTypeMerge && entityType != "work" {
		return apperr.BadRequest("validation.invalid_request", "only works can be merged")
	}
	var table string
	switch entityType {
	case "work":
		table = "book_works"
	case "edition":
		table = "book_editions"
	case "person":
		table = "book_people"
	case "publisher":
		table = "book_publishers"
	default:
		return apperr.BadRequest("validation.invalid_request", "entity_type is invalid")
	}
	var count int64
	if err := s.db.Table(table).Where("id = ?", *entityID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return apperr.NotFound("books.entity_not_found", "Book edit target not found")
	}
	if editType == model.BookEditTypeMerge {
		redirect, ok := payload["redirect_to"].(string)
		parsed, err := uuid.Parse(strings.TrimSpace(redirect))
		if !ok || err != nil || parsed == uuid.Nil {
			return apperr.BadRequest("validation.invalid_request", "redirect_to is required for merge")
		}
	}
	return nil
}

func validateBookEditInput(input SubmitBookEditInput) error {
	switch input.Type {
	case model.BookEditTypeCreate, model.BookEditTypeUpdate, model.BookEditTypeMerge, model.BookEditTypeRetire, model.BookEditTypeReopen:
	default:
		return apperr.BadRequest("validation.invalid_request", "edit type is invalid")
	}
	switch input.EntityType {
	case "work", "edition", "person", "publisher":
	default:
		return apperr.BadRequest("validation.invalid_request", "entity_type is invalid")
	}
	if len(input.Sources) == 0 {
		return apperr.BadRequest("books.source_required", "at least one source is required")
	}
	for _, source := range input.Sources {
		parsed, err := url.Parse(strings.TrimSpace(source.URL))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || len(source.URL) > 4096 {
			return apperr.BadRequest("validation.invalid_request", "book source URL is invalid")
		}
	}
	return nil
}

func (s *Service) applyBookEdit(tx *gorm.DB, edit *model.BookEdit) error {
	var payload map[string]any
	if err := json.Unmarshal([]byte(edit.PayloadJSON), &payload); err != nil {
		return apperr.Unprocessable("books.edit_payload_invalid", "Book edit payload is invalid")
	}
	if err := validateBookRelations(tx, edit.EntityType, payload); err != nil {
		return err
	}
	if edit.Type == model.BookEditTypeMerge {
		redirect, ok := payload["redirect_to"].(string)
		if !ok {
			return apperr.BadRequest("validation.invalid_request", "redirect_to is required for merge")
		}
		targetID, err := uuid.Parse(redirect)
		if err != nil || targetID == uuid.Nil {
			return apperr.BadRequest("validation.invalid_request", "redirect_to must be a valid UUID")
		}
		return mergeBookWorks(tx, *edit.EntityID, targetID)
	}
	if edit.Type == model.BookEditTypeCreate {
		return createBookEntity(tx, edit, payload)
	}
	if edit.EntityID == nil {
		return apperr.BadRequest("validation.invalid_request", "entity_id is required")
	}
	updates := map[string]any{}
	fields := map[string][]string{
		"work":      {"title", "subtitle", "original_title", "description", "language"},
		"edition":   {"title", "language", "publisher", "publisher_id", "isbn10", "isbn13", "binding", "cover_url", "page_count", "published_date"},
		"person":    {"name", "sort_name", "description"},
		"publisher": {"name", "sort_name", "description"},
	}
	for _, field := range fields[edit.EntityType] {
		if value, ok := payload[field]; ok {
			updates[field] = value
		}
	}
	if edit.EntityType == "edition" {
		if value, ok := updates["publisher"]; ok {
			name := strings.TrimSpace(fmt.Sprint(value))
			publisherID, err := findOrCreateBookPublisher(tx, name)
			if err != nil {
				return err
			}
			updates["publisher"] = name
			updates["publisher_id"] = publisherID
		}
		if value, ok := updates["publisher_id"]; ok {
			if value == "" || value == nil || value == (*uuid.UUID)(nil) {
				updates["publisher_id"] = nil
				updates["publisher"] = ""
			} else {
				var publisher model.BookPublisher
				if err := tx.First(&publisher, "id = ?", value).Error; err != nil {
					return err
				}
				updates["publisher"] = publisher.Name
			}
		}
	}
	if edit.Type == model.BookEditTypeRetire {
		updates["lifecycle_status"] = model.BookLifecycleStatusRetired
	}
	if edit.Type == model.BookEditTypeReopen {
		updates["lifecycle_status"] = model.BookLifecycleStatusActive
	}
	_, authorsChanged := payload["author_ids"]
	if len(updates) == 0 && !(edit.EntityType == "work" && authorsChanged) {
		return apperr.BadRequest("validation.invalid_request", "book edit has no changes")
	}
	var table string
	switch edit.EntityType {
	case "work":
		table = "book_works"
	case "edition":
		table = "book_editions"
	case "person":
		table = "book_people"
	case "publisher":
		table = "book_publishers"
	}
	if len(updates) > 0 {
		if err := tx.Table(table).Where("id = ?", *edit.EntityID).Updates(updates).Error; err != nil {
			return err
		}
	}
	if edit.EntityType == "work" && authorsChanged {
		ids, _ := bookAuthorIDs(payload["author_ids"])
		if err := tx.Where("work_id = ? AND edition_id IS NULL AND role = ?", *edit.EntityID, "author").Delete(&model.BookContribution{}).Error; err != nil {
			return err
		}
		for index, id := range ids {
			if err := tx.Create(&model.BookContribution{WorkID: edit.EntityID, PersonID: id, Role: "author", Position: index + 1}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func bookAuthorIDs(value any) ([]uuid.UUID, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var values []string
	if err := json.Unmarshal(encoded, &values); err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", "author_ids must be an array")
	}
	ids := make([]uuid.UUID, 0, len(values))
	seen := map[uuid.UUID]bool{}
	for _, value := range values {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || seen[id] {
			return nil, apperr.BadRequest("validation.invalid_request", "author_ids is invalid")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

func validateBookRelations(db *gorm.DB, entityType string, payload map[string]any) error {
	if entityType == "edition" {
		if value, ok := payload["publisher_id"]; ok && value != nil && value != "" {
			name, ok := value.(string)
			id, err := uuid.Parse(name)
			if !ok || err != nil || id == uuid.Nil {
				return apperr.BadRequest("validation.invalid_request", "publisher_id is invalid")
			}
			var count int64
			if err := db.Model(&model.BookPublisher{}).Where("id = ? AND lifecycle_status = ?", id, model.BookLifecycleStatusActive).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return apperr.BadRequest("validation.invalid_request", "publisher is unavailable")
			}
		}
	}
	if entityType == "work" {
		if value, ok := payload["author_ids"]; ok {
			ids, err := bookAuthorIDs(value)
			if err != nil {
				return err
			}
			var count int64
			if len(ids) > 0 {
				if err := db.Model(&model.BookPerson{}).Where("id IN ? AND lifecycle_status = ?", ids, model.BookLifecycleStatusActive).Count(&count).Error; err != nil {
					return err
				}
			}
			if int(count) != len(ids) {
				return apperr.BadRequest("validation.invalid_request", "author is unavailable")
			}
		}
	}
	return nil
}

func createBookEntity(tx *gorm.DB, edit *model.BookEdit, payload map[string]any) error {
	title, _ := payload["title"].(string)
	name, _ := payload["name"].(string)
	if edit.EntityType == "publisher" {
		if strings.TrimSpace(name) == "" {
			return apperr.BadRequest("validation.invalid_request", "publisher name is required")
		}
		publisher := model.BookPublisher{Name: name, SortName: payloadString(payload, "sort_name"), Description: payloadString(payload, "description"), LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment, CreatedBy: &edit.SubmittedBy}
		if err := tx.Create(&publisher).Error; err != nil {
			return err
		}
		edit.EntityID = &publisher.ID
		return updateBookEditSourceTargets(tx, edit.ID, publisher.ID)
	}
	if edit.EntityType == "person" {
		if strings.TrimSpace(name) == "" {
			return apperr.BadRequest("validation.invalid_request", "person name is required")
		}
		person := model.BookPerson{Name: strings.TrimSpace(name), SortName: payloadString(payload, "sort_name"), Description: payloadString(payload, "description"), LifecycleStatus: model.BookLifecycleStatusActive, CreatedBy: &edit.SubmittedBy}
		if err := tx.Create(&person).Error; err != nil {
			return err
		}
		edit.EntityID = &person.ID
		return updateBookEditSourceTargets(tx, edit.ID, person.ID)
	}
	if strings.TrimSpace(title) == "" {
		return apperr.BadRequest("validation.invalid_request", "work or edition title is required")
	}
	if edit.EntityType == "work" {
		work := model.BookWork{Title: strings.TrimSpace(title), Subtitle: payloadString(payload, "subtitle"), OriginalTitle: payloadString(payload, "original_title"), Description: payloadString(payload, "description"), Language: payloadString(payload, "language"), LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment, CreatedBy: &edit.SubmittedBy}
		if err := tx.Create(&work).Error; err != nil {
			return err
		}
		edit.EntityID = &work.ID
		return updateBookEditSourceTargets(tx, edit.ID, work.ID)
	}
	workID, err := uuid.Parse(payloadString(payload, "work_id"))
	if err != nil || workID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "edition work_id is required")
	}
	publisherName := payloadString(payload, "publisher")
	publisherID, err := findOrCreateBookPublisher(tx, publisherName)
	if err != nil {
		return err
	}
	edition := model.BookEdition{WorkID: workID, Title: strings.TrimSpace(title), PublisherID: publisherID, Publisher: publisherName, ISBN10: payloadString(payload, "isbn10"), ISBN13: payloadString(payload, "isbn13"), Language: payloadString(payload, "language"), Binding: payloadString(payload, "binding"), CoverURL: payloadString(payload, "cover_url"), LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment, CreatedBy: &edit.SubmittedBy}
	if err := tx.Create(&edition).Error; err != nil {
		return err
	}
	edit.EntityID = &edition.ID
	return updateBookEditSourceTargets(tx, edit.ID, edition.ID)
}

func mergeBookWorks(tx *gorm.DB, oldID, targetID uuid.UUID) error {
	if oldID == uuid.Nil || targetID == uuid.Nil || oldID == targetID {
		return apperr.BadRequest("validation.invalid_request", "merge targets are invalid")
	}
	var target model.BookWork
	if err := tx.Where("id = ? AND lifecycle_status = ?", targetID, model.BookLifecycleStatusActive).First(&target).Error; err != nil {
		return apperr.NotFound("books.merge_target_not_found", "Merge target is not an active work")
	}
	var old model.BookWork
	if err := tx.Where("id = ?", oldID).First(&old).Error; err != nil {
		return apperr.NotFound("books.entity_not_found", "Book work not found")
	}
	if err := migrateBookRatings(tx, oldID, targetID); err != nil {
		return err
	}
	if err := migrateBookReviews(tx, oldID, targetID); err != nil {
		return err
	}
	if err := migrateBookShelves(tx, oldID, targetID); err != nil {
		return err
	}
	if err := migrateBookPostLinks(tx, oldID, targetID); err != nil {
		return err
	}
	for _, value := range []any{&model.BookEdition{}, &model.BookContribution{}} {
		if err := tx.Model(value).Where("work_id = ?", oldID).Updates(map[string]any{"work_id": targetID}).Error; err != nil {
			return err
		}
	}
	if err := tx.Model(&model.BookSource{}).Where("target_type = ? AND target_id = ?", "work", oldID).Updates(map[string]any{"target_id": targetID}).Error; err != nil {
		return err
	}
	return tx.Model(&old).Updates(map[string]any{"lifecycle_status": model.BookLifecycleStatusMerged, "edit_status": model.BookEditStatusClosed, "redirect_to": targetID}).Error
}

func migrateBookRatings(tx *gorm.DB, oldID, targetID uuid.UUID) error {
	return migrateUniqueBookInteraction(tx, "book_ratings", oldID, targetID)
}
func migrateBookReviews(tx *gorm.DB, oldID, targetID uuid.UUID) error {
	return migrateUniqueBookInteraction(tx, "book_reviews", oldID, targetID)
}
func migrateBookShelves(tx *gorm.DB, oldID, targetID uuid.UUID) error {
	return migrateUniqueBookInteraction(tx, "user_book_shelves", oldID, targetID)
}

func migrateUniqueBookInteraction(tx *gorm.DB, table string, oldID, targetID uuid.UUID) error {
	var userIDs []string
	if err := tx.Table(table).Where("work_id = ?", oldID).Pluck("user_id", &userIDs).Error; err != nil {
		return err
	}
	for _, rawUserID := range userIDs {
		userID, err := uuid.Parse(rawUserID)
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Table(table).Where("user_id = ? AND work_id = ?", userID, targetID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			if err := tx.Exec("DELETE FROM "+table+" WHERE user_id = ? AND work_id = ?", userID, oldID).Error; err != nil {
				return err
			}
			continue
		}
		if err := tx.Table(table).Where("user_id = ? AND work_id = ?", userID, oldID).Updates(map[string]any{"work_id": targetID}).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateBookPostLinks(tx *gorm.DB, oldID, targetID uuid.UUID) error {
	var postIDs []string
	if err := tx.Table("book_post_links").Where("work_id = ?", oldID).Pluck("post_id", &postIDs).Error; err != nil {
		return err
	}
	for _, rawPostID := range postIDs {
		postID, err := uuid.Parse(rawPostID)
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Table("book_post_links").Where("work_id = ? AND post_id = ?", targetID, postID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			if err := tx.Exec("DELETE FROM book_post_links WHERE work_id = ? AND post_id = ?", oldID, postID).Error; err != nil {
				return err
			}
		} else if err := tx.Table("book_post_links").Where("work_id = ? AND post_id = ?", oldID, postID).Updates(map[string]any{"work_id": targetID}).Error; err != nil {
			return err
		}
	}
	return nil
}

func updateBookEditSourceTargets(tx *gorm.DB, editID, targetID uuid.UUID) error {
	return tx.Model(&model.BookSource{}).Where("book_edit_id = ?", editID).Updates(map[string]any{"target_id": targetID}).Error
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func buildBookEditDTO(db *gorm.DB, edit model.BookEdit) BookEditDTO {
	dto := BookEditDTO{ID: edit.ID.String(), Type: edit.Type, EntityType: edit.EntityType, Status: edit.Status, Reason: edit.Reason, DecisionNote: edit.DecisionNote, CreatedAt: edit.CreatedAt, ReviewedAt: edit.ReviewedAt, Sources: []BookPublicSourceDTO{}}
	_ = json.Unmarshal([]byte(edit.PayloadJSON), &dto.Payload)
	if edit.EntityID != nil {
		dto.EntityID = edit.EntityID.String()
	}
	if db.Migrator().HasTable(&model.BookEditVote{}) {
		dto.UpvoteCount, dto.DownvoteCount = bookEditVoteCounts(db, edit.ID)
	}
	var sources []model.BookSource
	if db.Where("book_edit_id = ?", edit.ID).Order("created_at ASC").Find(&sources).Error == nil {
		for _, source := range sources {
			dto.Sources = append(dto.Sources, buildBookPublicSourceDTO(source))
		}
	}
	return dto
}
