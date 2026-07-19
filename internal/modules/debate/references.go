package debate

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/resourceref"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type referenceKey struct {
	kind, id, qualifier string
}

type relationProjection struct {
	sourceID uuid.UUID
	stance   string
	state    string
	eventID  uuid.UUID
	indices  []int
}

func (s *Service) projectReferences(tx *gorm.DB, user authctx.CurrentUser, debate model.Debate, revision model.Revision, baseRevisionID uuid.UUID, lockedDebates map[uuid.UUID]model.Debate) ([]model.DebateRevisionReference, error) {
	parsed, err := resourceref.Parse(debate.Content)
	if err != nil {
		return nil, apperr.BadRequest("debate.reference_invalid", "Content contains an invalid resource reference")
	}
	baseByKey := map[referenceKey][]model.DebateRevisionReference{}
	if baseRevisionID != uuid.Nil {
		base, err := effectiveRevisionReferences(tx, baseRevisionID)
		if err != nil {
			return nil, err
		}
		for _, ref := range base {
			key := referenceKey{ref.Kind, ref.ResourceID.String(), ref.Qualifier}
			baseByKey[key] = append(baseByKey[key], ref)
		}
	}

	occurrences := map[referenceKey]int{}
	refs := make([]model.DebateRevisionReference, 0, len(parsed))
	groups := map[uuid.UUID]*relationProjection{}
	for _, parsedRef := range parsed {
		key := referenceKey{parsedRef.Kind, parsedRef.ResourceID.String(), parsedRef.Qualifier}
		occurrences[key]++
		occurrence := occurrences[key]
		var base *model.DebateRevisionReference
		if prior := baseByKey[key]; occurrence <= len(prior) {
			copy := prior[occurrence-1]
			base = &copy
		}
		resolvedRef, projection, err := s.resolveRevisionReference(tx, user, debate, parsedRef, occurrence, base, lockedDebates)
		if err != nil {
			return nil, err
		}
		resolvedRef.DebateID, resolvedRef.RevisionID = debate.ID, revision.ID
		refs = append(refs, resolvedRef)
		if projection != nil {
			group := groups[projection.sourceID]
			if group == nil {
				projection.indices = []int{len(refs) - 1}
				groups[projection.sourceID] = projection
			} else {
				if group.stance != projection.stance {
					return nil, apperr.Conflict("debate.reference_conflict", "A debate cannot be both supported and opposed")
				}
				if group.state == model.DebateRelationActive && projection.state != model.DebateRelationActive {
					group.state, group.eventID = projection.state, projection.eventID
				}
				group.indices = append(group.indices, len(refs)-1)
			}
		}
	}

	var existing []model.DebateRelation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("target_debate_id = ?", debate.ID).Find(&existing).Error; err != nil {
		return nil, err
	}
	existingBySource := make(map[uuid.UUID]*model.DebateRelation, len(existing))
	for i := range existing {
		existingBySource[existing[i].SourceDebateID] = &existing[i]
	}
	for sourceID, relation := range existingBySource {
		if _, keep := groups[sourceID]; !keep {
			if err := tx.Unscoped().Delete(&model.DebateRelation{}, "id = ?", relation.ID).Error; err != nil {
				return nil, err
			}
		}
	}

	groupIDs := make([]uuid.UUID, 0, len(groups))
	for sourceID := range groups {
		groupIDs = append(groupIDs, sourceID)
	}
	sort.Slice(groupIDs, func(i, j int) bool { return groupIDs[i].String() < groupIDs[j].String() })
	for _, sourceID := range groupIDs {
		group := groups[sourceID]
		relation := existingBySource[sourceID]
		if group.state == model.DebateRelationActive {
			path, err := activePath(tx, debate.ID, sourceID)
			if err != nil {
				return nil, err
			}
			if len(path) > 0 {
				cycle := append([]uuid.UUID{sourceID}, path...)
				appErr := apperr.Conflict("debate.reference_cycle", "This reference would create a cycle")
				appErr.Details["path"] = cycle
				return nil, appErr
			}
		}
		if relation == nil {
			if group.state != model.DebateRelationActive {
				return nil, apperr.BadRequest("debate.reference_unavailable", "Referenced debate is unavailable")
			}
			created := model.DebateRelation{
				SourceDebateID: sourceID, TargetDebateID: debate.ID, Stance: group.stance,
				TargetRevisionID: revision.ID, SourceConclusionEventID: group.eventID, Status: group.state,
			}
			if err := tx.Create(&created).Error; err != nil {
				return nil, err
			}
			relation = &created
		} else {
			updates := map[string]any{"stance": group.stance, "target_revision_id": revision.ID, "status": group.state}
			if group.state == model.DebateRelationActive {
				updates["source_conclusion_event_id"] = group.eventID
			}
			if err := tx.Model(relation).Updates(updates).Error; err != nil {
				return nil, err
			}
			relation.Stance, relation.TargetRevisionID, relation.Status = group.stance, revision.ID, group.state
			if group.state == model.DebateRelationActive {
				relation.SourceConclusionEventID = group.eventID
			}
		}
		for _, index := range group.indices {
			refs[index].RelationID = &relation.ID
			refs[index].State = relation.Status
		}
	}
	if len(refs) > 0 {
		if err := tx.Create(&refs).Error; err != nil {
			return nil, err
		}
	}
	return refs, nil
}

func (s *Service) resolveRevisionReference(tx *gorm.DB, user authctx.CurrentUser, debate model.Debate, parsed resourceref.Reference, occurrence int, base *model.DebateRevisionReference, lockedDebates map[uuid.UUID]model.Debate) (model.DebateRevisionReference, *relationProjection, error) {
	result := model.DebateRevisionReference{
		Raw: parsed.Raw, Kind: parsed.Kind, ResourceID: parsed.ResourceID,
		Qualifier: parsed.Qualifier, Occurrence: occurrence, State: model.DebateRelationActive,
	}
	if base != nil && (base.State == model.DebateRelationStale || base.State == model.DebateRelationUnavailable) {
		result.Title, result.State, result.RelationID = base.Title, base.State, base.RelationID
		if parsed.Kind == resourceref.KindDebate {
			projection := &relationProjection{sourceID: parsed.ResourceID, stance: parsed.Qualifier, state: base.State}
			if base.RelationID != nil {
				var relation model.DebateRelation
				if err := tx.First(&relation, "id = ?", *base.RelationID).Error; err == nil {
					projection.eventID = relation.SourceConclusionEventID
				}
			}
			return result, projection, nil
		}
		return result, nil, nil
	}

	if parsed.Kind == resourceref.KindDebate {
		if parsed.ResourceID == debate.ID {
			return model.DebateRevisionReference{}, nil, apperr.BadRequest("debate.reference_self", "A debate cannot reference itself")
		}
		source, found := lockedDebates[parsed.ResourceID]
		valid := found && source.Status == model.DebateStatusActive && source.CurrentConclusionEventID != nil
		if !valid {
			if base == nil {
				return model.DebateRevisionReference{}, nil, apperr.BadRequest("debate.reference_unavailable", "Referenced debate is unavailable")
			}
			result.Title, result.State, result.RelationID = base.Title, model.DebateRelationUnavailable, base.RelationID
			return result, &relationProjection{sourceID: parsed.ResourceID, stance: parsed.Qualifier, state: model.DebateRelationUnavailable}, nil
		}
		result.Title = source.Title
		return result, &relationProjection{sourceID: source.ID, stance: parsed.Qualifier, state: model.DebateRelationActive, eventID: *source.CurrentConclusionEventID}, nil
	}

	resolved, err := s.resources.Resolve(context.Background(), resourceref.Viewer{UserID: user.ID}, parsed.Kind, parsed.ResourceID)
	if err != nil || !resolved.Visible || !resolved.Referenceable {
		if base == nil {
			return model.DebateRevisionReference{}, nil, apperr.BadRequest("debate.reference_unavailable", "Referenced resource is unavailable")
		}
		result.Title, result.State = base.Title, model.DebateRelationUnavailable
		return result, nil, nil
	}
	result.Title = strings.TrimSpace(resolved.Title)
	return result, nil, nil
}

func (s *Service) attachCurrentReferences(debate model.Debate) (DebateDTO, error) {
	result := DebateDTO{Debate: debate, References: []DebateReferenceDTO{}}
	if debate.CurrentRevisionID == nil {
		return result, nil
	}
	refs, err := effectiveRevisionReferences(s.db, *debate.CurrentRevisionID)
	if err != nil {
		return DebateDTO{}, err
	}
	result.References = s.referenceDTOs(refs)
	return result, nil
}

func (s *Service) referenceDTOs(refs []model.DebateRevisionReference) []DebateReferenceDTO {
	result := make([]DebateReferenceDTO, 0, len(refs))
	for _, ref := range refs {
		state := ref.State
		if ref.RelationID == nil {
			resolved, err := s.resources.Resolve(context.Background(), resourceref.Viewer{}, ref.Kind, ref.ResourceID)
			if err != nil || !resolved.Visible || !resolved.Referenceable {
				state = model.DebateRelationUnavailable
			} else {
				state = model.DebateRelationActive
			}
		}
		result = append(result, DebateReferenceDTO{
			Raw: ref.Raw, Kind: ref.Kind, ResourceID: ref.ResourceID, Title: ref.Title,
			Qualifier: ref.Qualifier, State: state, RelationID: ref.RelationID,
		})
	}
	return result
}

func effectiveRevisionReferences(db *gorm.DB, revisionID uuid.UUID) ([]model.DebateRevisionReference, error) {
	var refs []model.DebateRevisionReference
	if err := db.Where("revision_id = ?", revisionID).Order("created_at ASC, id ASC").Find(&refs).Error; err != nil {
		return nil, err
	}
	for i := range refs {
		if refs[i].RelationID == nil {
			continue
		}
		var relation model.DebateRelation
		if err := db.First(&relation, "id = ?", *refs[i].RelationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				refs[i].State = model.DebateRelationUnavailable
				continue
			}
			return nil, err
		}
		refs[i].State = relation.Status
	}
	return refs, nil
}

// activePath returns a path from start to goal using current active directed
// relations, or nil when no path exists.
func activePath(db *gorm.DB, start, goal uuid.UUID) ([]uuid.UUID, error) {
	if start == goal {
		return []uuid.UUID{start}, nil
	}
	queue := []uuid.UUID{start}
	parent := map[uuid.UUID]uuid.UUID{}
	seen := map[uuid.UUID]bool{start: true}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		var next []uuid.UUID
		if err := db.Model(&model.DebateRelation{}).Where("source_debate_id = ? AND status = ?", current, model.DebateRelationActive).Pluck("target_debate_id", &next).Error; err != nil {
			return nil, err
		}
		for _, node := range next {
			if seen[node] {
				continue
			}
			seen[node], parent[node] = true, current
			if node == goal {
				path := []uuid.UUID{goal}
				for cursor := goal; cursor != start; {
					cursor = parent[cursor]
					path = append(path, cursor)
				}
				for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
					path[left], path[right] = path[right], path[left]
				}
				return path, nil
			}
			queue = append(queue, node)
		}
	}
	return nil, nil
}

func (s *Service) ReconfirmReference(user authctx.CurrentUser, debateID, relationID uuid.UUID, req ReconfirmReferenceRequest) (DebateDTO, error) {
	if err := s.requireActiveUser(user); err != nil {
		return DebateDTO{}, err
	}
	if strings.TrimSpace(req.EditSummary) == "" {
		return DebateDTO{}, apperr.BadRequest("validation.invalid_request", "edit_summary is required")
	}
	if req.BaseRevisionID == uuid.Nil {
		return DebateDTO{}, apperr.BadRequest("validation.invalid_request", "base_revision is required")
	}
	var identity model.DebateRelation
	if err := s.db.Select("id", "source_debate_id", "target_debate_id").First(&identity, "id = ?", relationID).Error; err != nil {
		return DebateDTO{}, apperr.NotFound("debate.relation_not_found", "Reference relation not found")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		lockedDebates, err := lockDebatesInOrder(tx, []uuid.UUID{identity.SourceDebateID, identity.TargetDebateID})
		if err != nil {
			return err
		}
		var relation model.DebateRelation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&relation, "id = ?", relationID).Error; err != nil {
			return apperr.NotFound("debate.relation_not_found", "Reference relation not found")
		}
		if relation.SourceDebateID != identity.SourceDebateID || relation.TargetDebateID != identity.TargetDebateID || relation.TargetDebateID != debateID {
			return apperr.NotFound("debate.relation_not_found", "Reference relation not found")
		}
		if relation.Status != model.DebateRelationStale {
			return apperr.Conflict("debate.reference_not_stale", "Only stale references can be reconfirmed")
		}
		debate, current, err := lockWikiForEdit(tx, lockedDebates, debateID, req.BaseRevisionID)
		if err != nil {
			return err
		}
		if err := ensureWikiEditable(tx, user, debate); err != nil {
			return err
		}
		source, found := lockedDebates[relation.SourceDebateID]
		if !found || source.Status != model.DebateStatusActive || source.CurrentConclusionEventID == nil {
			return apperr.BadRequest("debate.reference_unavailable", "Referenced debate is unavailable")
		}
		sourceEventID := *source.CurrentConclusionEventID
		path, err := activePath(tx, debate.ID, source.ID)
		if err != nil {
			return err
		}
		if len(path) > 0 {
			appErr := apperr.Conflict("debate.reference_cycle", "This reference would create a cycle")
			appErr.Details["path"] = append([]uuid.UUID{source.ID}, path...)
			return appErr
		}
		var snapshot DebateSnapshot
		if err := json.Unmarshal(current.ContentSnapshot, &snapshot); err != nil {
			return err
		}
		if err := tx.Model(&current).Update("is_current", false).Error; err != nil {
			return err
		}
		created, err := createRevision(tx, debate.ID, user.ID, &current.ID, current.VersionNumber+1, snapshot, strings.TrimSpace(req.EditSummary), "edit")
		if err != nil {
			return err
		}
		baseRefs, err := effectiveRevisionReferences(tx, current.ID)
		if err != nil {
			return err
		}
		for i := range baseRefs {
			baseRefs[i].Base = model.Base{}
			baseRefs[i].RevisionID = created.ID
			if baseRefs[i].RelationID != nil && *baseRefs[i].RelationID == relation.ID {
				baseRefs[i].State = model.DebateRelationActive
			}
		}
		if len(baseRefs) > 0 {
			if err := tx.Create(&baseRefs).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.DebateRelation{}).Where("target_debate_id = ?", debate.ID).Update("target_revision_id", created.ID).Error; err != nil {
			return err
		}
		if err := tx.Model(&relation).Updates(map[string]any{
			"status": model.DebateRelationActive, "source_conclusion_event_id": sourceEventID,
			"target_revision_id": created.ID,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&debate).Update("current_revision_id", created.ID).Error
	})
	if err != nil {
		return DebateDTO{}, err
	}
	return s.GetDebate(debateID)
}
