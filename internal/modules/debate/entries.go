package debate

import (
	"errors"
	"strings"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/modules/reference"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxDebateEntryTitleLength   = 200
	maxDebateEntryContentLength = 5000
)

func BuildDebateStats(db *gorm.DB, debateIDs []uuid.UUID) (map[uuid.UUID]DebateStatsDTO, error) {
	result := make(map[uuid.UUID]DebateStatsDTO, len(debateIDs))
	if len(debateIDs) == 0 {
		return result, nil
	}
	for _, id := range debateIDs {
		result[id] = DebateStatsDTO{}
	}

	if model.HasTableCached(db, &model.DebateEntry{}) {
		type entryRow struct {
			DebateID  uuid.UUID
			EntryType string
			Count     int
		}
		var rows []entryRow
		if err := db.Model(&model.DebateEntry{}).
			Select("debate_id, entry_type, COUNT(*) AS count").
			Where("debate_id IN ? AND status = ?", debateIDs, model.DebateEntryStatusActive).
			Group("debate_id, entry_type").
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			stats := result[row.DebateID]
			switch row.EntryType {
			case model.DebateEntryTypeClaim:
				stats.ClaimCount = row.Count
			case model.DebateEntryTypeEvidence:
				stats.ActiveEvidenceCount = row.Count
			case model.DebateEntryTypeRebuttal:
				stats.RebuttalCount = row.Count
			}
			result[row.DebateID] = stats
		}
	}

	if model.HasTableCached(db, &model.DebateVote{}) {
		type voteRow struct {
			DebateID uuid.UUID
			Count    int
		}
		var rows []voteRow
		if err := db.Model(&model.DebateVote{}).
			Select("debate_id, COUNT(*) AS count").
			Where("debate_id IN ?", debateIDs).
			Group("debate_id").
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			stats := result[row.DebateID]
			stats.VoteCount = row.Count
			result[row.DebateID] = stats
		}
	}

	if model.HasTableCached(db, &model.DebateEvidenceVote{}) {
		type evRow struct {
			DebateID uuid.UUID
			Rating   string
			Count    int
		}
		var rows []evRow
		if err := db.Model(&model.DebateEvidenceVote{}).
			Select("debate_id, rating, COUNT(*) AS count").
			Where("debate_id IN ?", debateIDs).
			Group("debate_id, rating").
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			stats := result[row.DebateID]
			switch row.Rating {
			case model.DebateEvidenceRatingSufficient:
				stats.EvidenceSufficient = row.Count
			case model.DebateEvidenceRatingPartial:
				stats.EvidencePartial = row.Count
			case model.DebateEvidenceRatingInsufficient:
				stats.EvidenceInsufficient = row.Count
			}
			result[row.DebateID] = stats
		}
	}

	for id, stats := range result {
		evidenceTotal := stats.EvidenceSufficient + stats.EvidencePartial + stats.EvidenceInsufficient
		stats.EvidenceWeak = stats.ActiveEvidenceCount == 0 || (evidenceTotal > 0 && stats.EvidenceInsufficient*2 > evidenceTotal)
		result[id] = stats
	}
	return result, nil
}

func (s *Service) AttachListStats(items []DebateListItemDTO) ([]DebateListItemDTO, error) {
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	statsByID, err := BuildDebateStats(s.db, ids)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if stats, ok := statsByID[items[i].ID]; ok {
			copyStats := stats
			items[i].Stats = &copyStats
		}
	}
	return items, nil
}

func (s *Service) ListEntries(debateID uuid.UUID) ([]DebateEntryDTO, error) {
	if debateID == uuid.Nil {
		return nil, apperr.BadRequest("validation.invalid_request", "debate_id is required")
	}
	if _, err := s.repo.GetDebate(debateID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.NotFound("debate.not_found", "Debate not found")
		}
		return nil, err
	}
	if !model.HasTableCached(s.db, &model.DebateEntry{}) {
		return []DebateEntryDTO{}, nil
	}
	var entries []model.DebateEntry
	if err := s.db.Preload("User").
		Where("debate_id = ?", debateID).
		Order("created_at ASC, id ASC").
		Find(&entries).Error; err != nil {
		return nil, err
	}
	return s.entryDTOs(debateID, entries)
}

func (s *Service) CreateEntry(user authctx.CurrentUser, debateID uuid.UUID, req CreateDebateEntryRequest) (DebateEntryDTO, error) {
	if err := s.requireActiveUser(user); err != nil {
		return DebateEntryDTO{}, err
	}
	if debateID == uuid.Nil {
		return DebateEntryDTO{}, apperr.BadRequest("validation.invalid_request", "debate_id is required")
	}
	entryType := strings.TrimSpace(req.EntryType)
	if entryType != model.DebateEntryTypeClaim && entryType != model.DebateEntryTypeEvidence && entryType != model.DebateEntryTypeRebuttal {
		return DebateEntryDTO{}, apperr.BadRequest("debate.entry_invalid", "invalid entry_type")
	}
	title := strings.TrimSpace(req.Title)
	content := strings.TrimSpace(req.Content)
	if utf8.RuneCountInString(title) > maxDebateEntryTitleLength {
		return DebateEntryDTO{}, apperr.BadRequest("debate.entry_invalid", "title is too long")
	}
	if content == "" || utf8.RuneCountInString(content) > maxDebateEntryContentLength {
		return DebateEntryDTO{}, apperr.BadRequest("debate.entry_invalid", "content is required and must fit within limit")
	}

	var createdID uuid.UUID
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var debate model.Debate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&debate, "id = ?", debateID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.not_found", "Debate not found")
			}
			return err
		}
		if debate.Status == model.DebateStatusArchived {
			return apperr.Conflict("debate.archived", "Archived debates cannot be modified")
		}

		stance, parentID, err := validateEntryParentAndStance(tx, debateID, entryType, req.ParentID, strings.TrimSpace(req.Stance), title)
		if err != nil {
			return err
		}

		contentRefs, err := resolveContentReferenceTokens(tx, debateID, "debate_entry", "content", content, entryType == model.DebateEntryTypeEvidence, nil)
		if err != nil {
			return err
		}

		entry := model.DebateEntry{
			DebateID:  debateID,
			ParentID:  parentID,
			UserID:    user.ID,
			EntryType: entryType,
			Stance:    stance,
			Title:     title,
			Content:   content,
			Status:    model.DebateEntryStatusActive,
		}
		if err := tx.Create(&entry).Error; err != nil {
			return err
		}
		if len(contentRefs) > 0 {
			for i := range contentRefs {
				contentRefs[i].SourceID = entry.ID
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&contentRefs).Error; err != nil {
				return err
			}
		}
		createdID = entry.ID
		return nil
	})
	if err != nil {
		return DebateEntryDTO{}, err
	}
	return s.loadSingleEntryDTO(debateID, createdID)
}

func (s *Service) UpdateEntry(user authctx.CurrentUser, debateID, entryID uuid.UUID, req UpdateDebateEntryRequest) (DebateEntryDTO, error) {
	if err := s.requireActiveUser(user); err != nil {
		return DebateEntryDTO{}, err
	}
	title := strings.TrimSpace(req.Title)
	content := strings.TrimSpace(req.Content)
	if utf8.RuneCountInString(title) > maxDebateEntryTitleLength {
		return DebateEntryDTO{}, apperr.BadRequest("debate.entry_invalid", "title is too long")
	}
	if content == "" || utf8.RuneCountInString(content) > maxDebateEntryContentLength {
		return DebateEntryDTO{}, apperr.BadRequest("debate.entry_invalid", "content is required and must fit within limit")
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var debate model.Debate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&debate, "id = ?", debateID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.not_found", "Debate not found")
			}
			return err
		}
		if debate.Status == model.DebateStatusArchived {
			return apperr.Conflict("debate.archived", "Archived debates cannot be modified")
		}

		var entry model.DebateEntry
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&entry, "id = ? AND debate_id = ?", entryID, debateID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.entry_not_found", "Debate entry not found")
			}
			return err
		}
		if entry.UserID != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
			return apperr.Forbidden("debate.entry_forbidden", "Not allowed to edit this entry")
		}
		if entry.Status == model.DebateEntryStatusRejected {
			return apperr.Conflict("debate.entry_rejected", "Rejected entries cannot be edited")
		}

		stance := strings.TrimSpace(req.Stance)
		if stance == "" {
			stance = entry.Stance
		}
		if entry.EntryType == model.DebateEntryTypeClaim {
			if title == "" {
				return apperr.BadRequest("debate.entry_invalid", "title is required for claim")
			}
			if stance != model.DebateEntryStanceSupport && stance != model.DebateEntryStanceOppose {
				return apperr.BadRequest("debate.entry_invalid", "claim stance must be support or oppose")
			}
		} else if stance != model.DebateEntryStanceSupport && stance != model.DebateEntryStanceOppose && stance != model.DebateEntryStanceNeutral {
			return apperr.BadRequest("debate.entry_invalid", "invalid entry stance")
		}

		inherited := make(map[referenceTargetKey]struct{})
		var oldRows []model.ContentReference
		if err := tx.Where("source_type = ? AND source_id = ? AND source_field = ?", "debate_entry", entry.ID, "content").
			Find(&oldRows).Error; err != nil {
			return err
		}
		for _, row := range oldRows {
			inherited[referenceTargetKey{kind: row.TargetType, id: row.TargetID}] = struct{}{}
		}

		contentRefs, err := resolveContentReferenceTokens(tx, debateID, "debate_entry", "content", content, entry.EntryType == model.DebateEntryTypeEvidence, inherited)
		if err != nil {
			return err
		}
		if err := tx.Unscoped().Where("source_type = ? AND source_id = ? AND source_field = ?", "debate_entry", entry.ID, "content").
			Delete(&model.ContentReference{}).Error; err != nil {
			return err
		}
		if len(contentRefs) > 0 {
			for i := range contentRefs {
				contentRefs[i].SourceID = entry.ID
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&contentRefs).Error; err != nil {
				return err
			}
		}

		return tx.Model(&entry).Updates(map[string]any{
			"stance":  stance,
			"title":   title,
			"content": content,
		}).Error
	})
	if err != nil {
		return DebateEntryDTO{}, err
	}
	return s.loadSingleEntryDTO(debateID, entryID)
}

func (s *Service) DeleteEntry(user authctx.CurrentUser, debateID, entryID uuid.UUID) error {
	if err := s.requireActiveUser(user); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var debate model.Debate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&debate, "id = ?", debateID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.not_found", "Debate not found")
			}
			return err
		}
		if debate.Status == model.DebateStatusArchived {
			return apperr.Conflict("debate.archived", "Archived debates cannot be modified")
		}

		var entry model.DebateEntry
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&entry, "id = ? AND debate_id = ?", entryID, debateID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.entry_not_found", "Debate entry not found")
			}
			return err
		}
		if entry.UserID != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
			return apperr.Forbidden("debate.entry_forbidden", "Not allowed to delete this entry")
		}

		allIDs := []uuid.UUID{entry.ID}
		var childIDs []uuid.UUID
		if err := tx.Model(&model.DebateEntry{}).Where("debate_id = ? AND parent_id = ?", debateID, entry.ID).Pluck("id", &childIDs).Error; err != nil {
			return err
		}
		if len(childIDs) > 0 {
			allIDs = append(allIDs, childIDs...)
			var grandchildIDs []uuid.UUID
			if err := tx.Model(&model.DebateEntry{}).Where("debate_id = ? AND parent_id IN ?", debateID, childIDs).Pluck("id", &grandchildIDs).Error; err != nil {
				return err
			}
			allIDs = append(allIDs, grandchildIDs...)
		}

		if err := tx.Where("id IN ?", allIDs).Delete(&model.DebateEntry{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_type = ? AND source_id IN ?", "debate_entry", allIDs).Delete(&model.ContentReference{}).Error; err != nil {
			return err
		}
		if model.HasTableCached(tx, &model.DebateChallenge{}) {
			now := tx.NowFunc()
			if err := tx.Model(&model.DebateChallenge{}).
				Where("debate_id = ? AND target_type = ? AND target_id IN ? AND status = ?", debateID, model.DebateChallengeTargetEntry, allIDs, model.DebateChallengeStatusOpen).
				Updates(map[string]any{"status": model.DebateChallengeStatusDismissed, "resolved_at": &now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func validateEntryParentAndStance(
	tx *gorm.DB,
	debateID uuid.UUID,
	entryType string,
	rawParentID *uuid.UUID,
	rawStance string,
	title string,
) (string, *uuid.UUID, error) {
	switch entryType {
	case model.DebateEntryTypeClaim:
		if rawParentID != nil && *rawParentID != uuid.Nil {
			return "", nil, apperr.BadRequest("debate.entry_invalid", "claim cannot have a parent_id")
		}
		if title == "" {
			return "", nil, apperr.BadRequest("debate.entry_invalid", "title is required for claim")
		}
		if rawStance != model.DebateEntryStanceSupport && rawStance != model.DebateEntryStanceOppose {
			return "", nil, apperr.BadRequest("debate.entry_invalid", "claim stance must be support or oppose")
		}
		return rawStance, nil, nil

	case model.DebateEntryTypeEvidence:
		var parent *model.DebateEntry
		var parentID *uuid.UUID
		if rawParentID != nil && *rawParentID != uuid.Nil {
			var found model.DebateEntry
			if err := tx.First(&found, "id = ? AND debate_id = ?", *rawParentID, debateID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return "", nil, apperr.BadRequest("debate.entry_invalid", "parent entry not found in debate")
				}
				return "", nil, err
			}
			if found.EntryType != model.DebateEntryTypeClaim {
				return "", nil, apperr.BadRequest("debate.entry_invalid", "evidence parent must be a claim")
			}
			if found.Status == model.DebateEntryStatusRejected {
				return "", nil, apperr.Conflict("debate.entry_rejected", "parent entry has been rejected")
			}
			parent = &found
			idCopy := found.ID
			parentID = &idCopy
		}
		stance := rawStance
		if stance == "" {
			if parent != nil {
				stance = parent.Stance
			} else {
				stance = model.DebateEntryStanceNeutral
			}
		}
		if stance != model.DebateEntryStanceSupport && stance != model.DebateEntryStanceOppose && stance != model.DebateEntryStanceNeutral {
			return "", nil, apperr.BadRequest("debate.entry_invalid", "invalid evidence stance")
		}
		return stance, parentID, nil

	case model.DebateEntryTypeRebuttal:
		if rawParentID == nil || *rawParentID == uuid.Nil {
			return "", nil, apperr.BadRequest("debate.entry_invalid", "parent_id is required for rebuttal")
		}
		var parent model.DebateEntry
		if err := tx.First(&parent, "id = ? AND debate_id = ?", *rawParentID, debateID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", nil, apperr.BadRequest("debate.entry_invalid", "parent entry not found in debate")
			}
			return "", nil, err
		}
		if parent.EntryType == model.DebateEntryTypeRebuttal {
			return "", nil, apperr.BadRequest("debate.entry_depth_exceeded", "rebuttals can only target claims or evidence")
		}
		if parent.Status == model.DebateEntryStatusRejected {
			return "", nil, apperr.Conflict("debate.entry_rejected", "parent entry has been rejected")
		}
		stance := rawStance
		if stance == "" {
			switch parent.Stance {
			case model.DebateEntryStanceSupport:
				stance = model.DebateEntryStanceOppose
			case model.DebateEntryStanceOppose:
				stance = model.DebateEntryStanceSupport
			default:
				stance = model.DebateEntryStanceNeutral
			}
		}
		if stance != model.DebateEntryStanceSupport && stance != model.DebateEntryStanceOppose && stance != model.DebateEntryStanceNeutral {
			return "", nil, apperr.BadRequest("debate.entry_invalid", "invalid rebuttal stance")
		}
		idCopy := parent.ID
		return stance, &idCopy, nil
	}
	return "", nil, apperr.BadRequest("debate.entry_invalid", "invalid entry_type")
}

func resolveContentReferenceTokens(
	tx *gorm.DB,
	debateID uuid.UUID,
	sourceType string,
	sourceField string,
	content string,
	requireResource bool,
	inherited map[referenceTargetKey]struct{},
) ([]model.ContentReference, error) {
	relationTokens, err := parseDebateReferences(content)
	if err != nil || len(relationTokens) > 0 {
		return nil, apperr.BadRequest("debate.reference_invalid", "Content contains an invalid resource reference")
	}

	parsed, err := reference.Parse(content)
	if err != nil {
		return nil, apperr.BadRequest("debate.reference_invalid", "Content contains an invalid resource reference")
	}

	registry := reference.NewRegistry(tx)
	validated := make(map[referenceTargetKey]struct{})
	rows := make([]model.ContentReference, 0, len(parsed))
	for _, item := range parsed {
		if item.Kind != reference.KindResource {
			continue
		}
		targetID, err := uuid.Parse(item.Identifier)
		if err != nil {
			return nil, apperr.BadRequest("debate.reference_invalid", "Content contains an invalid resource reference")
		}
		if item.TargetType == "debate" && targetID == debateID {
			return nil, apperr.BadRequest("debate.reference_self", "A debate cannot reference itself")
		}
		key := referenceTargetKey{kind: item.TargetType, id: targetID}
		if _, ok := validated[key]; !ok {
			_, resolveErr := registry.Resolve(reference.Viewer{}, item.TargetType, targetID)
			if resolveErr != nil {
				if errors.Is(resolveErr, reference.ErrTargetUnavailable) {
					if _, keep := inherited[key]; !keep {
						return nil, apperr.BadRequest("debate.reference_unavailable", "Referenced resource is unavailable")
					}
				} else {
					return nil, resolveErr
				}
			} else if item.TargetType == "debate" {
				if _, keep := inherited[key]; !keep {
					var targetDebate model.Debate
					if err := tx.Select("status").First(&targetDebate, "id = ?", targetID).Error; err != nil {
						if errors.Is(err, gorm.ErrRecordNotFound) {
							return nil, apperr.BadRequest("debate.reference_unavailable", "Referenced resource is unavailable")
						}
						return nil, err
					}
					if targetDebate.Status != model.DebateStatusActive {
						return nil, apperr.BadRequest("debate.reference_unavailable", "Referenced resource is unavailable")
					}
				}
			}
			validated[key] = struct{}{}
		}
		rows = append(rows, model.ContentReference{
			SourceType:  sourceType,
			SourceField: sourceField,
			TargetType:  item.TargetType,
			TargetID:    targetID,
			StartOffset: item.Start,
			EndOffset:   item.End,
		})
	}

	if requireResource && len(rows) == 0 {
		return nil, apperr.BadRequest("debate.evidence_reference_required", "Evidence entries must cite at least one internal resource")
	}
	return rows, nil
}

func (s *Service) loadSingleEntryDTO(debateID, entryID uuid.UUID) (DebateEntryDTO, error) {
	var entry model.DebateEntry
	if err := s.db.Preload("User").First(&entry, "id = ? AND debate_id = ?", entryID, debateID).Error; err != nil {
		return DebateEntryDTO{}, err
	}
	dtos, err := s.entryDTOs(debateID, []model.DebateEntry{entry})
	if err != nil {
		return DebateEntryDTO{}, err
	}
	if len(dtos) == 0 {
		return DebateEntryDTO{}, gorm.ErrRecordNotFound
	}
	return dtos[0], nil
}

func (s *Service) entryDTOs(debateID uuid.UUID, entries []model.DebateEntry) ([]DebateEntryDTO, error) {
	if len(entries) == 0 {
		return []DebateEntryDTO{}, nil
	}
	ids := make([]uuid.UUID, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}

	refsByEntry := map[uuid.UUID][]reference.ResolvedReference{}
	if model.HasTableCached(s.db, &model.ContentReference{}) {
		var rows []model.ContentReference
		if err := s.db.Where("source_type = ? AND source_id IN ? AND source_field = ?", "debate_entry", ids, "content").
			Order("start_offset ASC, id ASC").Find(&rows).Error; err != nil {
			return nil, err
		}
		resolved, err := s.referenceService().ResolveStoredRows(s.db, reference.Viewer{}, rows)
		if err != nil {
			return nil, err
		}
		refsByEntry = resolved
	}

	openChallenges := map[uuid.UUID]DebateChallengeSummaryDTO{}
	if model.HasTableCached(s.db, &model.DebateChallenge{}) {
		var challenges []model.DebateChallenge
		if err := s.db.Where("debate_id = ? AND target_type = ? AND target_id IN ? AND status = ?",
			debateID, model.DebateChallengeTargetEntry, ids, model.DebateChallengeStatusOpen).
			Find(&challenges).Error; err != nil {
			return nil, err
		}
		for _, ch := range challenges {
			openChallenges[ch.TargetID] = DebateChallengeSummaryDTO{
				ID:           ch.ID,
				Status:       ch.Status,
				ReasonCode:   ch.ReasonCode,
				UpholdVotes:  ch.UpholdVotes,
				DismissVotes: ch.DismissVotes,
			}
		}
	}

	result := make([]DebateEntryDTO, 0, len(entries))
	for _, entry := range entries {
		refs := refsByEntry[entry.ID]
		if refs == nil {
			refs = []reference.ResolvedReference{}
		}
		dto := DebateEntryDTO{
			ID:                  entry.ID,
			DebateID:            entry.DebateID,
			ParentID:            entry.ParentID,
			UserID:              entry.UserID,
			User:                model.PublicUserFromUser(entry.User),
			EntryType:           entry.EntryType,
			Stance:              entry.Stance,
			Title:               entry.Title,
			Content:             entry.Content,
			Status:              entry.Status,
			RejectedChallengeID: entry.RejectedChallengeID,
			References:          refs,
			CreatedAt:           entry.CreatedAt,
			UpdatedAt:           entry.UpdatedAt,
		}
		if ch, ok := openChallenges[entry.ID]; ok {
			copyCh := ch
			dto.OpenChallenge = &copyCh
		}
		result = append(result, dto)
	}
	return result, nil
}
