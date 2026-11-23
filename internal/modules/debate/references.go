package debate

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"atoman/internal/model"
	"atoman/internal/modules/reference"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

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
	parsed, err := parseDebateReferences(debate.Content)
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
		key := referenceKey{"debate", parsedRef.ResourceID.String(), parsedRef.Stance}
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
		} else if relation.Status == model.DebateRelationRejected {
			group.state = model.DebateRelationRejected
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
		contentRefs := make([]model.ContentReference, len(refs))
		for i, ref := range refs {
			contentRefs[i] = model.ContentReference{SourceType: "debate_revision", SourceID: revision.ID, SourceField: "content", TargetType: "debate", TargetID: ref.ResourceID, StartOffset: parsed[i].Start, EndOffset: parsed[i].End}
		}
		if err := tx.Create(&contentRefs).Error; err != nil {
			return nil, err
		}
		for i := range refs {
			refs[i].ContentReferenceID = contentRefs[i].ID
		}
		if err := tx.Create(&refs).Error; err != nil {
			return nil, err
		}
	}
	if err := projectNonRelationReferences(tx, debate, revision.ID, baseRevisionID); err != nil {
		return nil, err
	}
	return refs, nil
}

func projectNonRelationReferences(tx *gorm.DB, debate model.Debate, revisionID, baseRevisionID uuid.UUID) error {
	parsed, err := reference.Parse(debate.Content)
	if err != nil {
		return apperr.BadRequest("debate.reference_invalid", "Content contains an invalid resource reference")
	}
	if len(parsed) == 0 {
		return nil
	}
	type targetKey struct {
		kind string
		id   uuid.UUID
	}
	inherited := make(map[targetKey]struct{})
	if baseRevisionID != uuid.Nil {
		var priorRows []model.ContentReference
		if err := tx.Where("source_type = ? AND source_id = ? AND source_field = ?", "debate_revision", baseRevisionID, "content").
			Find(&priorRows).Error; err != nil {
			return err
		}
		for _, row := range priorRows {
			inherited[targetKey{kind: row.TargetType, id: row.TargetID}] = struct{}{}
		}
	}

	registry := reference.NewRegistry(tx)
	validated := make(map[targetKey]struct{})
	rows := make([]model.ContentReference, 0, len(parsed))
	for _, item := range parsed {
		if item.Kind != reference.KindResource {
			continue
		}
		targetID, err := uuid.Parse(item.Identifier)
		if err != nil {
			return apperr.BadRequest("debate.reference_invalid", "Content contains an invalid resource reference")
		}
		if item.TargetType == "debate" && targetID == debate.ID {
			return apperr.BadRequest("debate.reference_self", "A debate cannot reference itself")
		}
		key := targetKey{kind: item.TargetType, id: targetID}
		if _, ok := validated[key]; !ok {
			_, resolveErr := registry.Resolve(reference.Viewer{}, item.TargetType, targetID)
			if resolveErr != nil {
				if errors.Is(resolveErr, reference.ErrTargetUnavailable) {
					if _, keep := inherited[key]; !keep {
						return apperr.BadRequest("debate.reference_unavailable", "Referenced resource is unavailable")
					}
				} else {
					return resolveErr
				}
			} else if item.TargetType == "debate" {
				if _, keep := inherited[key]; !keep {
					var targetDebate model.Debate
					if err := tx.Select("status").First(&targetDebate, "id = ?", targetID).Error; err != nil {
						if errors.Is(err, gorm.ErrRecordNotFound) {
							return apperr.BadRequest("debate.reference_unavailable", "Referenced resource is unavailable")
						}
						return err
					}
					if targetDebate.Status != model.DebateStatusActive {
						return apperr.BadRequest("debate.reference_unavailable", "Referenced resource is unavailable")
					}
				}
			}
			validated[key] = struct{}{}
		}
		rows = append(rows, model.ContentReference{
			SourceType:  "debate_revision",
			SourceID:    revisionID,
			SourceField: "content",
			TargetType:  item.TargetType,
			TargetID:    targetID,
			StartOffset: item.Start,
			EndOffset:   item.End,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

func (s *Service) resolveRevisionReference(tx *gorm.DB, _ authctx.CurrentUser, debate model.Debate, parsed debateReferenceToken, occurrence int, base *model.DebateRevisionReference, lockedDebates map[uuid.UUID]model.Debate) (model.DebateRevisionReference, *relationProjection, error) {
	result := model.DebateRevisionReference{
		Raw: parsed.Raw, Kind: "debate", ResourceID: parsed.ResourceID,
		Qualifier: parsed.Stance, Occurrence: occurrence, State: model.DebateRelationActive,
	}
	if base != nil && (base.State == model.DebateRelationStale || base.State == model.DebateRelationUnavailable || base.State == model.DebateRelationRejected) {
		result.Title, result.State, result.RelationID = base.Title, base.State, base.RelationID
		{
			projection := &relationProjection{sourceID: parsed.ResourceID, stance: parsed.Stance, state: base.State}
			if base.RelationID != nil {
				var relation model.DebateRelation
				if err := tx.First(&relation, "id = ?", *base.RelationID).Error; err == nil {
					projection.eventID = relation.SourceConclusionEventID
				}
			}
			return result, projection, nil
		}
	}

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
		return result, &relationProjection{sourceID: parsed.ResourceID, stance: parsed.Stance, state: model.DebateRelationUnavailable}, nil
	}
	result.Title = source.Title
	return result, &relationProjection{sourceID: source.ID, stance: parsed.Stance, state: model.DebateRelationActive, eventID: *source.CurrentConclusionEventID}, nil
}

func (s *Service) attachCurrentReferences(debate model.Debate) (DebateDTO, error) {
	result := DebateDTO{Debate: debate, User: model.PublicUserFromUser(debate.User), References: []DebateReferenceDTO{}}
	if statsByID, err := BuildDebateStats(s.db, []uuid.UUID{debate.ID}); err != nil {
		return DebateDTO{}, err
	} else if stats, ok := statsByID[debate.ID]; ok {
		result.Stats = &stats
	}
	if debate.CurrentRevisionID == nil {
		return result, nil
	}
	refs, err := effectiveRevisionReferences(s.db, *debate.CurrentRevisionID)
	if err != nil {
		return DebateDTO{}, err
	}
	result.References, err = s.referenceDTOs(debate.ID, *debate.CurrentRevisionID, refs)
	if err != nil {
		return DebateDTO{}, err
	}
	return result, nil
}

func (s *Service) referenceService() *reference.Service {
	if s.references != nil {
		return s.references
	}
	return reference.NewService(s.db)
}

func (s *Service) referenceDTOs(debateID, revisionID uuid.UUID, refs []model.DebateRevisionReference) ([]DebateReferenceDTO, error) {
	ids := make([]uuid.UUID, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ContentReferenceID)
	}
	rows := map[uuid.UUID]model.ContentReference{}
	if len(ids) > 0 {
		var stored []model.ContentReference
		if err := s.db.Where("id IN ?", ids).Find(&stored).Error; err != nil {
			return nil, err
		}
		for _, row := range stored {
			rows[row.ID] = row
		}
	}

	relationChallenges, referenceChallengesByRowID, referenceChallengesByTarget, err := s.loadReferenceChallenges(debateID)
	if err != nil {
		return nil, err
	}

	registry := reference.NewRegistry(s.db)
	result := make([]DebateReferenceDTO, 0, len(refs))
	for _, ref := range refs {
		row, ok := rows[ref.ContentReferenceID]
		if !ok {
			return nil, gorm.ErrRecordNotFound
		}
		target := DebateReferenceTargetDTO{
			Type:      row.TargetType,
			ID:        row.TargetID,
			Label:     ref.Title,
			Module:    "debate",
			Path:      "/" + row.TargetID.String(),
			Available: ref.State == model.DebateRelationActive,
		}
		if resolved, err := registry.Resolve(reference.Viewer{}, row.TargetType, row.TargetID); err == nil {
			target.Subtitle = resolved.Subtitle
			target.Excerpt = resolved.Excerpt
			target.SourceName = resolved.SourceName
			target.PublishedAt = resolved.PublishedAt
		}
		item := DebateReferenceDTO{
			ID: row.ID, Field: row.SourceField, Start: row.StartOffset, End: row.EndOffset, Raw: ref.Raw,
			Target:     target,
			ResourceID: row.TargetID, Title: ref.Title, Qualifier: ref.Qualifier, State: ref.State, RelationID: ref.RelationID,
		}
		if ref.RelationID != nil {
			var relation model.DebateRelation
			if err := s.db.First(&relation, "id = ?", *ref.RelationID).Error; err == nil {
				item.Relation = &DebateReferenceRelationDTO{ID: relation.ID, Stance: relation.Stance, State: relation.Status, ConclusionEventID: relation.SourceConclusionEventID}
				var event model.DebateConclusionEvent
				if err := s.db.First(&event, "id = ?", relation.SourceConclusionEventID).Error; err == nil {
					item.Relation.ConclusionDirection = event.Direction
				}
			}
			if ch, ok := relationChallenges[*ref.RelationID]; ok {
				copyCh := ch
				item.Challenge = &copyCh
				if ch.Status == model.DebateChallengeStatusUpheld && item.Relation != nil {
					item.Relation.State = model.DebateRelationRejected
					item.State = model.DebateRelationRejected
					item.Target.Available = false
				}
			}
		}
		result = append(result, item)
	}

	var storedAll []model.ContentReference
	if err := s.db.Where("source_type = ? AND source_id = ?", "debate_revision", revisionID).
		Order("start_offset ASC, end_offset ASC, id ASC").
		Find(&storedAll).Error; err != nil {
		return nil, err
	}
	nonRelationRows := make([]model.ContentReference, 0, len(storedAll))
	for _, row := range storedAll {
		if _, isRelation := rows[row.ID]; isRelation {
			continue
		}
		nonRelationRows = append(nonRelationRows, row)
	}
	if len(nonRelationRows) > 0 {
		resolvedByRevision, err := s.referenceService().ResolveStoredRows(s.db, reference.Viewer{}, nonRelationRows)
		if err != nil {
			return nil, err
		}
		resolvedList := resolvedByRevision[revisionID]
		for i, row := range nonRelationRows {
			if i >= len(resolvedList) {
				break
			}
			res := resolvedList[i]
			item := DebateReferenceDTO{
				ID:    row.ID,
				Field: row.SourceField,
				Start: res.Start,
				End:   res.End,
				Raw:   "@" + res.TargetType + ":" + res.TargetID.String(),
				Target: DebateReferenceTargetDTO{
					Type:        res.TargetType,
					ID:          res.TargetID,
					Label:       res.Label,
					Subtitle:    res.Subtitle,
					Excerpt:     res.Excerpt,
					SourceName:  res.SourceName,
					PublishedAt: res.PublishedAt,
					Module:      res.Module,
					Path:        res.Path,
					Available:   res.Available,
				},
				Relation:   nil,
				ResourceID: res.TargetID,
				Title:      res.Label,
			}
			if ch, ok := referenceChallengesByRowID[row.ID]; ok {
				copyCh := ch
				item.Challenge = &copyCh
			} else if ch, ok := referenceChallengesByTarget[referenceTargetKey{kind: row.TargetType, id: row.TargetID}]; ok {
				copyCh := ch
				item.Challenge = &copyCh
			}
			if item.Challenge != nil && item.Challenge.Status == model.DebateChallengeStatusUpheld {
				item.State = model.DebateRelationRejected
			}
			result = append(result, item)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Start != result[j].Start {
			return result[i].Start < result[j].Start
		}
		if result[i].End != result[j].End {
			return result[i].End < result[j].End
		}
		return result[i].ID.String() < result[j].ID.String()
	})
	return result, nil
}

type referenceTargetKey struct {
	kind string
	id   uuid.UUID
}

func (s *Service) loadReferenceChallenges(debateID uuid.UUID) (
	map[uuid.UUID]DebateReferenceChallengeDTO,
	map[uuid.UUID]DebateReferenceChallengeDTO,
	map[referenceTargetKey]DebateReferenceChallengeDTO,
	error,
) {
	relationChallenges := map[uuid.UUID]DebateReferenceChallengeDTO{}
	refByRowID := map[uuid.UUID]DebateReferenceChallengeDTO{}
	refByTarget := map[referenceTargetKey]DebateReferenceChallengeDTO{}

	if debateID == uuid.Nil || !model.HasTableCached(s.db, &model.DebateChallenge{}) {
		return relationChallenges, refByRowID, refByTarget, nil
	}

	var challenges []model.DebateChallenge
	if err := s.db.Where(
		"debate_id = ? AND target_type IN ? AND status IN ?",
		debateID,
		[]string{model.DebateChallengeTargetRelation, model.DebateChallengeTargetReference},
		[]string{model.DebateChallengeStatusOpen, model.DebateChallengeStatusUpheld},
	).Order("created_at DESC, id DESC").Find(&challenges).Error; err != nil {
		return nil, nil, nil, err
	}

	refRowIDs := make([]uuid.UUID, 0)
	for _, ch := range challenges {
		summary := DebateReferenceChallengeDTO{
			ID:           ch.ID,
			Status:       ch.Status,
			ReasonCode:   ch.ReasonCode,
			UpholdVotes:  ch.UpholdVotes,
			DismissVotes: ch.DismissVotes,
		}
		if ch.TargetType == model.DebateChallengeTargetRelation {
			if existing, exists := relationChallenges[ch.TargetID]; !exists || summary.Status == model.DebateChallengeStatusUpheld && existing.Status != model.DebateChallengeStatusUpheld {
				relationChallenges[ch.TargetID] = summary
			}
			continue
		}
		if existing, exists := refByRowID[ch.TargetID]; !exists || summary.Status == model.DebateChallengeStatusUpheld && existing.Status != model.DebateChallengeStatusUpheld {
			refByRowID[ch.TargetID] = summary
		}
		refRowIDs = append(refRowIDs, ch.TargetID)
	}

	if len(refRowIDs) > 0 && model.HasTableCached(s.db, &model.ContentReference{}) {
		var challengedRows []model.ContentReference
		if err := s.db.Where("id IN ?", refRowIDs).Find(&challengedRows).Error; err != nil {
			return nil, nil, nil, err
		}
		rowTargetByID := make(map[uuid.UUID]referenceTargetKey, len(challengedRows))
		for _, row := range challengedRows {
			rowTargetByID[row.ID] = referenceTargetKey{kind: row.TargetType, id: row.TargetID}
		}
		for _, ch := range challenges {
			if ch.TargetType != model.DebateChallengeTargetReference {
				continue
			}
			key, ok := rowTargetByID[ch.TargetID]
			if !ok {
				continue
			}
			summary := refByRowID[ch.TargetID]
			if existing, exists := refByTarget[key]; !exists || summary.Status == model.DebateChallengeStatusUpheld && existing.Status != model.DebateChallengeStatusUpheld {
				refByTarget[key] = summary
			}
		}
	}

	return relationChallenges, refByRowID, refByTarget, nil
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
		var oldContentRefs []model.ContentReference
		if err := tx.Where("source_type = ? AND source_id = ?", "debate_revision", current.ID).
			Order("start_offset ASC, end_offset ASC, id ASC").
			Find(&oldContentRefs).Error; err != nil {
			return err
		}
		copiedIDs := make(map[uuid.UUID]uuid.UUID, len(oldContentRefs))
		for _, old := range oldContentRefs {
			copy := model.ContentReference{SourceType: "debate_revision", SourceID: created.ID, SourceField: old.SourceField,
				TargetType: old.TargetType, TargetID: old.TargetID, StartOffset: old.StartOffset, EndOffset: old.EndOffset}
			if err := tx.Create(&copy).Error; err != nil {
				return err
			}
			copiedIDs[old.ID] = copy.ID
		}
		for i := range baseRefs {
			baseRefs[i].Base = model.Base{}
			baseRefs[i].RevisionID = created.ID
			baseRefs[i].ContentReferenceID = copiedIDs[baseRefs[i].ContentReferenceID]
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
