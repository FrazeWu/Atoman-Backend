package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/authctx"
	timelinecore "atoman/internal/timeline"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrTimelineProposalInvalid    = errors.New("invalid timeline revision proposal")
	ErrTimelineProposalForbidden  = errors.New("timeline revision proposal forbidden")
	ErrTimelineProposalNotFound   = errors.New("timeline revision proposal not found")
	ErrTimelineProposalNotPending = errors.New("timeline revision proposal is not pending")
)

type TimelineProposalInput struct {
	Content       string
	Evidence      string
	Patch         map[string]any
	Mentions      []comment.MentionInput
	AttachmentIDs []uuid.UUID
}

type TimelineProposal struct {
	Comment           comment.CommentDTO `json:"comment"`
	TargetKind        string             `json:"target_kind"`
	TargetID          uuid.UUID          `json:"target_id"`
	Patch             map[string]any     `json:"patch"`
	Evidence          string             `json:"evidence"`
	Status            string             `json:"status"`
	ReviewerID        *uuid.UUID         `json:"reviewer_id,omitempty"`
	AppliedRevisionID *uuid.UUID         `json:"applied_revision_id,omitempty"`
}

type TimelineProposalList struct {
	Items   []TimelineProposal `json:"items"`
	Page    int                `json:"page"`
	PerPage int                `json:"per_page"`
	Total   int                `json:"total"`
	HasMore bool               `json:"has_more"`
}

type TimelineRevisionProposalService struct {
	db       *gorm.DB
	comments *comment.Service
}

func NewTimelineRevisionProposalService(db *gorm.DB) *TimelineRevisionProposalService {
	return &TimelineRevisionProposalService{db: db, comments: comment.NewService(db, comment.NewTargetRegistry(db))}
}

func (s *TimelineRevisionProposalService) CreateEventProposal(user authctx.CurrentUser, eventID uuid.UUID, input TimelineProposalInput) (TimelineProposal, error) {
	return s.create(user, comment.TargetKindTimelineEvent, eventID, input)
}

func (s *TimelineRevisionProposalService) CreatePersonProposal(user authctx.CurrentUser, personID uuid.UUID, input TimelineProposalInput) (TimelineProposal, error) {
	return s.create(user, comment.TargetKindTimelinePerson, personID, input)
}

func (s *TimelineRevisionProposalService) List(user authctx.CurrentUser, kind string, targetID uuid.UUID, page, pageSize int) (TimelineProposalList, error) {
	if kind != comment.TargetKindTimelineEvent && kind != comment.TargetKindTimelinePerson {
		return TimelineProposalList{}, ErrTimelineProposalInvalid
	}
	if page < 1 {
		page = 1
	}
	roots, err := s.comments.List(user, comment.TargetRef{Kind: kind, ResourceID: targetID}, comment.ListCommentsInput{Page: page, PageSize: pageSize, Sort: comment.SortNewest})
	if err != nil {
		return TimelineProposalList{}, err
	}
	ids := make([]uuid.UUID, 0, len(roots.Items))
	for _, item := range roots.Items {
		ids = append(ids, item.ID)
	}
	extensions := make([]model.TimelineRevisionProposal, 0, len(ids))
	if len(ids) > 0 {
		if err := s.db.Where("comment_id IN ? AND target_kind = ? AND target_id = ?", ids, kind, targetID).Find(&extensions).Error; err != nil {
			return TimelineProposalList{}, err
		}
	}
	byID := make(map[uuid.UUID]model.TimelineRevisionProposal, len(extensions))
	for _, extension := range extensions {
		byID[extension.CommentID] = extension
	}
	items := make([]TimelineProposal, 0, len(extensions))
	for _, root := range roots.Items {
		extension, ok := byID[root.ID]
		if !ok {
			continue
		}
		var patch map[string]any
		if err := json.Unmarshal(extension.PatchJSON, &patch); err != nil {
			return TimelineProposalList{}, err
		}
		items = append(items, TimelineProposal{Comment: root, TargetKind: extension.TargetKind, TargetID: extension.TargetID, Patch: patch, Evidence: extension.Evidence, Status: extension.Status, ReviewerID: extension.ReviewerID, AppliedRevisionID: extension.AppliedRevisionID})
	}
	return TimelineProposalList{Items: items, Page: roots.Page, PerPage: roots.PerPage, Total: roots.TotalRoots, HasMore: roots.Page*roots.PerPage < roots.TotalRoots}, nil
}

func (s *TimelineRevisionProposalService) create(user authctx.CurrentUser, kind string, targetID uuid.UUID, input TimelineProposalInput) (TimelineProposal, error) {
	evidence := strings.TrimSpace(input.Evidence)
	if evidence == "" || len(input.Patch) == 0 {
		return TimelineProposal{}, ErrTimelineProposalInvalid
	}
	patch, err := s.normalizePatch(s.db, kind, targetID, input.Patch, false)
	if err != nil {
		return TimelineProposal{}, err
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return TimelineProposal{}, ErrTimelineProposalInvalid
	}
	created, err := s.comments.CreateWithExtension(user,
		comment.TargetRef{Kind: kind, ResourceID: targetID},
		comment.CreateCommentInput{Content: input.Content, Mentions: input.Mentions, AttachmentIDs: input.AttachmentIDs},
		func(tx *gorm.DB, entry *model.CommentEntry) error {
			if _, err := s.normalizePatch(tx, kind, targetID, patch, true); err != nil {
				return err
			}
			return tx.Create(&model.TimelineRevisionProposal{
				CommentID: entry.ID, TargetKind: kind, TargetID: targetID, PatchJSON: patchJSON, Evidence: evidence, Status: "pending",
			}).Error
		})
	if err != nil {
		return TimelineProposal{}, err
	}
	proposal, err := s.load(user, created.ID)
	proposal.Comment = created
	return proposal, err
}

func (s *TimelineRevisionProposalService) Decide(user authctx.CurrentUser, commentID uuid.UUID, decision string) (TimelineProposal, error) {
	if decision != "accept" && decision != "reject" {
		return TimelineProposal{}, ErrTimelineProposalInvalid
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var proposal model.TimelineRevisionProposal
		if err := tx.First(&proposal, "comment_id = ?", commentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTimelineProposalNotFound
			}
			return err
		}
		ownerID, err := lockTimelineProposalTarget(tx, proposal.TargetKind, proposal.TargetID)
		if err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&proposal, "comment_id = ?", commentID).Error; err != nil {
			return err
		}
		if ownerID != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
			return ErrTimelineProposalForbidden
		}
		if proposal.Status != "pending" {
			return ErrTimelineProposalNotPending
		}

		var patch map[string]any
		if err := json.Unmarshal(proposal.PatchJSON, &patch); err != nil {
			return ErrTimelineProposalInvalid
		}
		var revisionID *uuid.UUID
		if decision == "accept" {
			id, err := s.applyPatch(tx, proposal.TargetKind, proposal.TargetID, user.ID, patch)
			if err != nil {
				return err
			}
			revisionID = &id
		}
		action := "timeline_proposal." + decision
		metadata, _ := json.Marshal(map[string]any{"target_kind": proposal.TargetKind, "target_id": proposal.TargetID, "patch": patch})
		if err := tx.Create(&model.AuditLog{ActorID: &user.ID, Action: action, EntityType: "timeline_revision_proposal", EntityID: &commentID, Metadata: string(metadata)}).Error; err != nil {
			return err
		}
		updates := map[string]any{"status": decision + "ed", "reviewer_id": user.ID}
		if decision == "reject" {
			updates["status"] = "rejected"
		}
		if revisionID != nil {
			updates["applied_revision_id"] = *revisionID
		}
		return tx.Model(&proposal).Updates(updates).Error
	})
	if err != nil {
		return TimelineProposal{}, err
	}
	return s.load(user, commentID)
}

func lockTimelineProposalTarget(tx *gorm.DB, kind string, id uuid.UUID) (uuid.UUID, error) {
	switch kind {
	case comment.TargetKindTimelineEvent:
		var target model.TimelineEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&target, "id = ?", id).Error; err != nil {
			return uuid.Nil, ErrTimelineProposalNotFound
		}
		return target.UserID, nil
	case comment.TargetKindTimelinePerson:
		var target model.TimelinePerson
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&target, "id = ?", id).Error; err != nil {
			return uuid.Nil, ErrTimelineProposalNotFound
		}
		return target.UserID, nil
	default:
		return uuid.Nil, ErrTimelineProposalInvalid
	}
}

func (s *TimelineRevisionProposalService) normalizePatch(db *gorm.DB, kind string, id uuid.UUID, raw map[string]any, locked bool) (map[string]any, error) {
	query := db
	if locked {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	normalized := make(map[string]any, len(raw))
	changed := false
	switch kind {
	case comment.TargetKindTimelineEvent:
		var target model.TimelineEvent
		if err := query.First(&target, "id = ?", id).Error; err != nil {
			return nil, ErrTimelineProposalNotFound
		}
		for field, value := range raw {
			next, current, err := normalizeEventField(field, value, target)
			if err != nil {
				return nil, err
			}
			normalized[field] = next
			changed = changed || !reflect.DeepEqual(next, current)
		}
	case comment.TargetKindTimelinePerson:
		var target model.TimelinePerson
		if err := query.First(&target, "id = ?", id).Error; err != nil {
			return nil, ErrTimelineProposalNotFound
		}
		for field, value := range raw {
			next, current, err := normalizePersonField(field, value, target)
			if err != nil {
				return nil, err
			}
			normalized[field] = next
			changed = changed || !reflect.DeepEqual(next, current)
		}
	default:
		return nil, ErrTimelineProposalInvalid
	}
	if !changed {
		return nil, ErrTimelineProposalInvalid
	}
	return normalized, nil
}

func normalizeEventField(field string, value any, target model.TimelineEvent) (any, any, error) {
	switch field {
	case "title":
		return stringValue(value, true, target.Title)
	case "description":
		return stringValue(value, false, target.Description)
	case "content":
		return stringValue(value, false, target.Content)
	case "event_date":
		return dateValue(value, false, target.EventDate)
	case "end_date":
		return dateValue(value, true, target.EndDate)
	case "location":
		return stringValue(value, true, target.Location)
	case "latitude":
		return floatValue(value, target.Latitude)
	case "longitude":
		return floatValue(value, target.Longitude)
	case "source":
		return stringValue(value, true, target.Source)
	case "category":
		return stringValue(value, false, target.Category)
	case "tags":
		return tagsValue(value, target.Tags)
	default:
		return nil, nil, ErrTimelineProposalInvalid
	}
}

func normalizePersonField(field string, value any, target model.TimelinePerson) (any, any, error) {
	switch field {
	case "name":
		return stringValue(value, true, target.Name)
	case "bio":
		return stringValue(value, false, target.Bio)
	case "birth_date":
		return dateValue(value, true, target.BirthDate)
	case "death_date":
		return dateValue(value, true, target.DeathDate)
	case "tags":
		return tagsValue(value, target.Tags)
	default:
		return nil, nil, ErrTimelineProposalInvalid
	}
}

func stringValue(value any, required bool, current string) (any, any, error) {
	next, ok := value.(string)
	if !ok {
		return nil, nil, ErrTimelineProposalInvalid
	}
	next = strings.TrimSpace(next)
	if required && next == "" {
		return nil, nil, ErrTimelineProposalInvalid
	}
	return next, current, nil
}
func floatValue(value any, current *float64) (any, any, error) {
	if value == nil {
		return nil, current, nil
	}
	next, ok := value.(float64)
	if !ok {
		return nil, nil, ErrTimelineProposalInvalid
	}
	if current == nil {
		return next, nil, nil
	}
	return next, *current, nil
}
func tagsValue(value any, current []string) (any, any, error) {
	var next []string
	switch tags := value.(type) {
	case []string:
		next = tags
	case []any:
		for _, item := range tags {
			text, ok := item.(string)
			if !ok {
				return nil, nil, ErrTimelineProposalInvalid
			}
			next = append(next, strings.TrimSpace(text))
		}
	default:
		return nil, nil, ErrTimelineProposalInvalid
	}
	return next, current, nil
}
func dateValue(value any, optional bool, current any) (any, any, error) {
	if value == nil && optional {
		return nil, formatCurrentDate(current), nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, nil, ErrTimelineProposalInvalid
	}
	parsed, err := parseTimelineProposalDate(text)
	if err != nil {
		return nil, nil, ErrTimelineProposalInvalid
	}
	return parsed.Format(time.RFC3339), formatCurrentDate(current), nil
}
func formatCurrentDate(value any) any {
	switch date := value.(type) {
	case time.Time:
		return date.Format(time.RFC3339)
	case *time.Time:
		if date == nil {
			return nil
		}
		return date.Format(time.RFC3339)
	default:
		return nil
	}
}
func parseTimelineProposalDate(value string) (time.Time, error) {
	return timelinecore.ParseDateTime(value)
}

func (s *TimelineRevisionProposalService) applyPatch(tx *gorm.DB, kind string, id, reviewerID uuid.UUID, raw map[string]any) (uuid.UUID, error) {
	patch, err := s.normalizePatch(tx, kind, id, raw, false)
	if err != nil {
		return uuid.Nil, err
	}
	switch kind {
	case comment.TargetKindTimelineEvent:
		var event model.TimelineEvent
		if err := tx.First(&event, "id = ?", id).Error; err != nil {
			return uuid.Nil, err
		}
		applyEventPatch(&event, patch)
		if err := tx.Save(&event).Error; err != nil {
			return uuid.Nil, err
		}
		revision := eventRevisionSnapshot(event, reviewerID)
		if err := tx.Create(&revision).Error; err != nil {
			return uuid.Nil, err
		}
		return revision.ID, nil
	case comment.TargetKindTimelinePerson:
		var person model.TimelinePerson
		if err := tx.First(&person, "id = ?", id).Error; err != nil {
			return uuid.Nil, err
		}
		applyPersonPatch(&person, patch)
		if err := tx.Save(&person).Error; err != nil {
			return uuid.Nil, err
		}
		snapshot, err := json.Marshal(person)
		if err != nil {
			return uuid.Nil, err
		}
		var last int
		if err := tx.Model(&model.Revision{}).Where("content_type = ? AND content_id = ?", "timeline_person", id).Select("COALESCE(MAX(version_number), 0)").Scan(&last).Error; err != nil {
			return uuid.Nil, err
		}
		if err := tx.Model(&model.Revision{}).Where("content_type = ? AND content_id = ? AND is_current = ?", "timeline_person", id, true).Update("is_current", false).Error; err != nil {
			return uuid.Nil, err
		}
		now := time.Now()
		revision := model.Revision{ContentType: "timeline_person", ContentID: id, VersionNumber: last + 1, ContentSnapshot: snapshot, EditorID: reviewerID, EditSummary: "Accepted revision proposal", EditType: "edit", Status: "approved", ReviewerID: &reviewerID, ReviewedAt: &now, IsCurrent: true}
		if err := tx.Create(&revision).Error; err != nil {
			return uuid.Nil, err
		}
		return revision.ID, nil
	default:
		return uuid.Nil, ErrTimelineProposalInvalid
	}
}

func applyEventPatch(target *model.TimelineEvent, patch map[string]any) {
	for field, value := range patch {
		switch field {
		case "title":
			target.Title = value.(string)
		case "description":
			target.Description = value.(string)
		case "content":
			target.Content = value.(string)
		case "event_date":
			parsed, _ := timelinecore.ParseDateTime(value.(string))
			target.EventDate = parsed
		case "end_date":
			if value == nil {
				target.EndDate = nil
			} else {
				parsed, _ := timelinecore.ParseDateTime(value.(string))
				target.EndDate = &parsed
			}
		case "location":
			target.Location = value.(string)
		case "latitude":
			target.Latitude = optionalFloat(value)
		case "longitude":
			target.Longitude = optionalFloat(value)
		case "source":
			target.Source = value.(string)
		case "category":
			target.Category = value.(string)
		case "tags":
			target.Tags = toStrings(value)
		}
	}
}
func applyPersonPatch(target *model.TimelinePerson, patch map[string]any) {
	for field, value := range patch {
		switch field {
		case "name":
			target.Name = value.(string)
		case "bio":
			target.Bio = value.(string)
		case "birth_date":
			target.BirthDate = optionalDate(value)
		case "death_date":
			target.DeathDate = optionalDate(value)
		case "tags":
			target.Tags = toStrings(value)
		}
	}
}
func optionalFloat(value any) *float64 {
	if value == nil {
		return nil
	}
	next := value.(float64)
	return &next
}
func optionalDate(value any) *time.Time {
	if value == nil {
		return nil
	}
	next, _ := timelinecore.ParseDateTime(value.(string))
	return &next
}
func toStrings(value any) []string {
	values, _ := value.([]any)
	result := make([]string, 0, len(values))
	for _, item := range values {
		result = append(result, item.(string))
	}
	return result
}
func eventRevisionSnapshot(event model.TimelineEvent, editor uuid.UUID) model.TimelineRevision {
	end := ""
	if event.EndDate != nil {
		end = event.EndDate.Format("2006-01-02")
	}
	return model.TimelineRevision{EventID: event.ID, EditorID: editor, Title: event.Title, Description: event.Description, Content: event.Content, EventDate: event.EventDate.Format("2006-01-02"), EndDate: end, Location: event.Location, Source: event.Source, Category: event.Category, IsPublic: event.IsPublic}
}

func (s *TimelineRevisionProposalService) load(user authctx.CurrentUser, commentID uuid.UUID) (TimelineProposal, error) {
	var extension model.TimelineRevisionProposal
	if err := s.db.First(&extension, "comment_id = ?", commentID).Error; err != nil {
		return TimelineProposal{}, ErrTimelineProposalNotFound
	}
	var patch map[string]any
	if err := json.Unmarshal(extension.PatchJSON, &patch); err != nil {
		return TimelineProposal{}, fmt.Errorf("decode proposal patch: %w", err)
	}
	viewer := comment.Viewer{}
	if user.ID != uuid.Nil {
		viewer.UserID = &user.ID
	}
	entry, err := s.comments.Get(viewer, commentID)
	if err != nil {
		return TimelineProposal{}, err
	}
	return TimelineProposal{Comment: entry, TargetKind: extension.TargetKind, TargetID: extension.TargetID, Patch: patch, Evidence: extension.Evidence, Status: extension.Status, ReviewerID: extension.ReviewerID, AppliedRevisionID: extension.AppliedRevisionID}, nil
}
