package reference

import (
	"errors"
	"fmt"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct {
	db       *gorm.DB
	registry *Registry
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db, registry: NewRegistry(db)}
}

func (s *Service) Search(viewer Viewer, targetType, query string, limit int) ([]Target, error) {
	targetType = strings.TrimSpace(targetType)
	if targetType != TargetTypeUser && !IsSupportedResourceType(targetType) {
		return nil, apperr.BadRequest("reference.unsupported_type", "Unsupported reference type")
	}
	return s.registry.Search(viewer, targetType, query, limit)
}

func (s *Service) ResolvePreview(viewer Viewer, content string) ([]ResolvedReference, error) {
	parsed := ParseLenient(content)
	result := make([]ResolvedReference, 0, len(parsed))
	for _, item := range parsed {
		resolved := ResolvedReference{Kind: item.Kind, TargetType: item.TargetType, Start: item.Start, End: item.End}
		var target Target
		var err error
		if item.Kind == KindUser {
			target, err = s.registry.ResolveUsername(viewer, item.Identifier)
		} else {
			resolved.TargetID, _ = uuid.Parse(item.Identifier)
			target, err = s.registry.Resolve(viewer, item.TargetType, resolved.TargetID)
		}
		if err != nil {
			if errors.Is(err, ErrTargetUnavailable) {
				result = append(result, resolved)
				continue
			}
			return nil, err
		}
		resolved.TargetID = target.ID
		resolved.Label = target.Label
		resolved.Subtitle = target.Subtitle
		resolved.Module = target.Module
		resolved.Path = target.Path
		resolved.Available = true
		result = append(result, resolved)
	}
	return result, nil
}

func (s *Service) ReplacePublished(tx *gorm.DB, source Source, fields []Field) ([]ResolvedReference, error) {
	if source.ID == uuid.Nil || source.ActorID == uuid.Nil || strings.TrimSpace(source.Type) == "" {
		return nil, apperr.BadRequest("reference.invalid_source", "Invalid reference source")
	}
	registry := NewRegistry(tx)
	resourceViewer := Viewer{UserID: source.ActorID}
	if source.Audience == AudiencePublic {
		resourceViewer = Viewer{}
	}

	rows := make([]model.ContentReference, 0)
	resolvedItems := make([]ResolvedReference, 0)
	for _, field := range fields {
		if strings.TrimSpace(field.Name) == "" {
			return nil, apperr.BadRequest("reference.invalid_source", "Reference field is required")
		}
		parsed, err := Parse(field.Content)
		if err != nil {
			return nil, apperr.BadRequest("reference.invalid_syntax", "Reference syntax is invalid")
		}
		for _, item := range parsed {
			target, targetID, err := resolveStrictTarget(registry, resourceViewer, item)
			if err != nil {
				if errors.Is(err, ErrTargetUnavailable) {
					return nil, apperr.BadRequest("reference.invalid_target", "Reference is unavailable")
				}
				return nil, err
			}
			rows = append(rows, model.ContentReference{
				SourceType: source.Type, SourceID: source.ID, SourceField: field.Name,
				TargetType: item.TargetType, TargetID: targetID,
				StartOffset: item.Start, EndOffset: item.End,
			})
			resolvedItems = append(resolvedItems, resolvedFromParsed(item, target, field.Name))
		}
	}

	var oldRows []model.ContentReference
	if err := tx.Where("source_type = ? AND source_id = ?", source.Type, source.ID).Find(&oldRows).Error; err != nil {
		return nil, err
	}
	oldMentionTargets := userTargetSet(oldRows)
	if err := tx.Unscoped().Where("source_type = ? AND source_id = ?", source.Type, source.ID).Delete(&model.ContentReference{}).Error; err != nil {
		return nil, fmt.Errorf("replace content references: %w", err)
	}
	if len(rows) > 0 {
		if err := tx.Create(&rows).Error; err != nil {
			return nil, fmt.Errorf("create content references: %w", err)
		}
	}
	if err := notifyNewMentions(tx, source, rows, oldMentionTargets); err != nil {
		return nil, err
	}
	return resolvedItems, nil
}

func (s *Service) RemoveSource(tx *gorm.DB, sourceType string, sourceID uuid.UUID) error {
	return tx.Unscoped().Where("source_type = ? AND source_id = ?", sourceType, sourceID).Delete(&model.ContentReference{}).Error
}

func (s *Service) BackfillAvailable(tx *gorm.DB, source Source, fields []Field) error {
	registry := NewRegistry(tx)
	viewer := Viewer{UserID: source.ActorID}
	if source.Audience == AudiencePublic {
		viewer = Viewer{}
	}
	rows := make([]model.ContentReference, 0)
	for _, field := range fields {
		for _, item := range ParseLenient(field.Content) {
			_, targetID, err := resolveStrictTarget(registry, viewer, item)
			if errors.Is(err, ErrTargetUnavailable) {
				continue
			}
			if err != nil {
				return err
			}
			rows = append(rows, model.ContentReference{
				SourceType: source.Type, SourceID: source.ID, SourceField: field.Name,
				TargetType: item.TargetType, TargetID: targetID,
				StartOffset: item.Start, EndOffset: item.End,
			})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

func (s *Service) ResolveStoredRows(db *gorm.DB, viewer Viewer, rows []model.ContentReference) (map[uuid.UUID][]ResolvedReference, error) {
	registry := NewRegistry(db)
	result := make(map[uuid.UUID][]ResolvedReference)
	for _, row := range rows {
		kind := KindResource
		var target Target
		var err error
		if row.TargetType == TargetTypeUser {
			kind = KindUser
			target, err = registry.ResolveUserID(viewer, row.TargetID)
		} else {
			target, err = registry.Resolve(viewer, row.TargetType, row.TargetID)
		}
		item := ResolvedReference{
			Kind: kind, TargetType: row.TargetType, TargetID: row.TargetID,
			Field: row.SourceField, Start: row.StartOffset, End: row.EndOffset,
		}
		if err != nil {
			if errors.Is(err, ErrTargetUnavailable) {
				result[row.SourceID] = append(result[row.SourceID], item)
				continue
			}
			return nil, err
		}
		item.Label = target.Label
		item.Subtitle = target.Subtitle
		item.Module = target.Module
		item.Path = target.Path
		item.Available = true
		result[row.SourceID] = append(result[row.SourceID], item)
	}
	return result, nil
}

func resolveStrictTarget(registry *Registry, viewer Viewer, item ParsedReference) (Target, uuid.UUID, error) {
	if item.Kind == KindUser {
		target, err := registry.ResolveUsername(Viewer{}, item.Identifier)
		return target, target.ID, err
	}
	id, err := uuid.Parse(item.Identifier)
	if err != nil {
		return Target{}, uuid.Nil, ErrTargetUnavailable
	}
	target, err := registry.Resolve(viewer, item.TargetType, id)
	return target, id, err
}

func resolvedFromParsed(item ParsedReference, target Target, field string) ResolvedReference {
	return ResolvedReference{
		Kind: item.Kind, TargetType: item.TargetType, TargetID: target.ID,
		Field: field, Start: item.Start, End: item.End, Label: target.Label, Subtitle: target.Subtitle,
		Module: target.Module, Path: target.Path, Available: true,
	}
}

func userTargetSet(rows []model.ContentReference) map[uuid.UUID]struct{} {
	result := make(map[uuid.UUID]struct{})
	for _, row := range rows {
		if row.TargetType == TargetTypeUser {
			result[row.TargetID] = struct{}{}
		}
	}
	return result
}

func notifyNewMentions(tx *gorm.DB, source Source, rows []model.ContentReference, oldTargets map[uuid.UUID]struct{}) error {
	if source.SuppressMentionNotifications {
		return nil
	}
	seen := map[uuid.UUID]struct{}{source.ActorID: {}}
	notificationType := source.MentionNotificationType
	if notificationType == "" {
		notificationType = "content_mention"
	}
	notificationSourceType := source.NotificationSourceType
	if notificationSourceType == "" {
		notificationSourceType = "content_reference_" + source.Type
	}
	for _, row := range rows {
		if row.TargetType != TargetTypeUser {
			continue
		}
		if _, exists := oldTargets[row.TargetID]; exists {
			continue
		}
		if _, exists := seen[row.TargetID]; exists {
			continue
		}
		seen[row.TargetID] = struct{}{}
		meta := model.NotificationMeta{"source_type": source.Type, "source_id": source.ID.String()}
		for key, value := range source.Meta {
			meta[key] = value
		}
		notification := model.Notification{
			RecipientID: row.TargetID, ActorID: &source.ActorID, Type: notificationType,
			SourceType: notificationSourceType, SourceID: source.ID, Meta: meta,
		}
		result := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "recipient_id"}, {Name: "source_type"}, {Name: "source_id"}},
			TargetWhere: clause.Where{Exprs: []clause.Expression{
				clause.Expr{SQL: "aggregation_key = '' AND deleted_at IS NULL"},
			}},
			DoNothing: true,
		}).Create(&notification)
		if result.Error != nil {
			return fmt.Errorf("create mention notification: %w", result.Error)
		}
	}
	return nil
}
