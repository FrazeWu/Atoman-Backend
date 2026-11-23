package debate

import (
	"errors"
	"strings"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/modules/reference"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/service"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxDebateChallengeRationaleLength = 2000

func (s *Service) ListChallenges(user authctx.CurrentUser, debateID uuid.UUID, status string) ([]DebateChallengeDTO, error) {
	if debateID == uuid.Nil {
		return nil, apperr.BadRequest("validation.invalid_request", "debate_id is required")
	}
	if _, err := s.repo.GetDebate(debateID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.NotFound("debate.not_found", "Debate not found")
		}
		return nil, err
	}
	if !model.HasTableCached(s.db, &model.DebateChallenge{}) {
		return []DebateChallengeDTO{}, nil
	}

	query := s.db.Preload("Creator").Where("debate_id = ?", debateID)
	switch strings.TrimSpace(status) {
	case "":
	case model.DebateChallengeStatusOpen, model.DebateChallengeStatusUpheld, model.DebateChallengeStatusDismissed:
		query = query.Where("status = ?", strings.TrimSpace(status))
	case "resolved":
		query = query.Where("status IN ?", []string{model.DebateChallengeStatusUpheld, model.DebateChallengeStatusDismissed})
	default:
		return nil, apperr.BadRequest("debate.challenge_invalid", "invalid challenge status filter")
	}

	var challenges []model.DebateChallenge
	if err := query.Order("created_at DESC, id DESC").Find(&challenges).Error; err != nil {
		return nil, err
	}
	return s.challengeDTOs(user.ID, challenges)
}

func (s *Service) CreateChallenge(user authctx.CurrentUser, debateID uuid.UUID, req CreateChallengeRequest) (DebateChallengeDTO, error) {
	if err := s.requireActiveUser(user); err != nil {
		return DebateChallengeDTO{}, err
	}
	if debateID == uuid.Nil || req.TargetID == uuid.Nil {
		return DebateChallengeDTO{}, apperr.BadRequest("debate.challenge_invalid", "debate_id and target_id are required")
	}
	targetType := strings.TrimSpace(req.TargetType)
	if targetType != model.DebateChallengeTargetRelation &&
		targetType != model.DebateChallengeTargetReference &&
		targetType != model.DebateChallengeTargetEntry {
		return DebateChallengeDTO{}, apperr.BadRequest("debate.challenge_invalid", "invalid challenge target_type")
	}
	reasonCode := strings.TrimSpace(req.ReasonCode)
	switch reasonCode {
	case model.DebateChallengeReasonMismatch,
		model.DebateChallengeReasonOutdated,
		model.DebateChallengeReasonLowReliability,
		model.DebateChallengeReasonIrrelevant:
	default:
		return DebateChallengeDTO{}, apperr.BadRequest("debate.challenge_invalid", "invalid reason_code")
	}
	rationale := strings.TrimSpace(req.Rationale)
	if rationale == "" || utf8.RuneCountInString(rationale) > maxDebateChallengeRationaleLength {
		return DebateChallengeDTO{}, apperr.BadRequest("debate.challenge_invalid", "rationale is required and must fit within limit")
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
			return apperr.Conflict("debate.archived", "Archived debates cannot be challenged")
		}

		if err := validateChallengeTarget(tx, debate, targetType, req.TargetID); err != nil {
			return err
		}

		contentRefs, err := resolveContentReferenceTokens(tx, debateID, "debate_challenge", "rationale", rationale, false, nil)
		if err != nil {
			return err
		}

		challenge := model.DebateChallenge{
			DebateID:   debateID,
			TargetType: targetType,
			TargetID:   req.TargetID,
			CreatedBy:  user.ID,
			ReasonCode: reasonCode,
			Rationale:  rationale,
			Status:     model.DebateChallengeStatusOpen,
		}
		if err := tx.Create(&challenge).Error; err != nil {
			if isUniqueViolation(err) {
				return apperr.Conflict("debate.challenge_already_open", "An open challenge already exists for this target")
			}
			return err
		}
		if len(contentRefs) > 0 {
			for i := range contentRefs {
				contentRefs[i].SourceID = challenge.ID
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&contentRefs).Error; err != nil {
				return err
			}
		}
		createdID = challenge.ID
		return nil
	})
	if err != nil {
		return DebateChallengeDTO{}, err
	}
	return s.loadSingleChallengeDTO(user.ID, debateID, createdID)
}

func (s *Service) VoteChallenge(user authctx.CurrentUser, debateID, challengeID uuid.UUID, decision string) (DebateChallengeDTO, error) {
	if err := s.requireActiveUser(user); err != nil {
		return DebateChallengeDTO{}, err
	}
	decision = strings.TrimSpace(decision)
	if decision != model.DebateChallengeVoteUphold && decision != model.DebateChallengeVoteDismiss {
		return DebateChallengeDTO{}, apperr.BadRequest("validation.invalid_request", "decision must be uphold or dismiss")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var challenge model.DebateChallenge
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&challenge, "id = ? AND debate_id = ?", challengeID, debateID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.challenge_not_found", "Challenge not found")
			}
			return err
		}
		if challenge.Status != model.DebateChallengeStatusOpen {
			return apperr.Conflict("debate.challenge_closed", "Challenge has already been resolved")
		}

		var vote model.DebateChallengeVote
		res := tx.Unscoped().Where("challenge_id = ? AND user_id = ?", challengeID, user.ID).Limit(1).Find(&vote)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			vote = model.DebateChallengeVote{ChallengeID: challengeID, UserID: user.ID, Decision: decision}
			if err := tx.Create(&vote).Error; err != nil {
				return err
			}
		} else if err := tx.Unscoped().Model(&vote).Updates(map[string]any{"decision": decision, "deleted_at": nil}).Error; err != nil {
			return err
		}

		return s.recomputeAndEvaluateChallenge(tx, &challenge)
	})
	if err != nil {
		return DebateChallengeDTO{}, err
	}
	return s.loadSingleChallengeDTO(user.ID, debateID, challengeID)
}

func (s *Service) DeleteChallengeVote(user authctx.CurrentUser, debateID, challengeID uuid.UUID) (DebateChallengeDTO, error) {
	if err := s.requireActiveUser(user); err != nil {
		return DebateChallengeDTO{}, err
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var challenge model.DebateChallenge
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&challenge, "id = ? AND debate_id = ?", challengeID, debateID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.challenge_not_found", "Challenge not found")
			}
			return err
		}
		if challenge.Status != model.DebateChallengeStatusOpen {
			return apperr.Conflict("debate.challenge_closed", "Challenge has already been resolved")
		}

		var vote model.DebateChallengeVote
		if err := tx.Where("challenge_id = ? AND user_id = ?", challengeID, user.ID).First(&vote).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.vote_not_found", "Vote not found")
			}
			return err
		}
		if err := tx.Delete(&vote).Error; err != nil {
			return err
		}

		return s.recomputeAndEvaluateChallenge(tx, &challenge)
	})
	if err != nil {
		return DebateChallengeDTO{}, err
	}
	return s.loadSingleChallengeDTO(user.ID, debateID, challengeID)
}

func validateChallengeTarget(tx *gorm.DB, debate model.Debate, targetType string, targetID uuid.UUID) error {
	var openCount int64
	if err := tx.Model(&model.DebateChallenge{}).
		Where("debate_id = ? AND target_type = ? AND target_id = ? AND status = ?", debate.ID, targetType, targetID, model.DebateChallengeStatusOpen).
		Count(&openCount).Error; err != nil {
		return err
	}
	if openCount > 0 {
		return apperr.Conflict("debate.challenge_already_open", "An open challenge already exists for this target")
	}

	switch targetType {
	case model.DebateChallengeTargetRelation:
		var relation model.DebateRelation
		if err := tx.First(&relation, "id = ? AND target_debate_id = ?", targetID, debate.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.BadRequest("debate.challenge_invalid", "Referenced relation not found on this debate")
			}
			return err
		}
		if relation.Status == model.DebateRelationRejected || relation.Status == model.DebateRelationUnavailable {
			return apperr.BadRequest("debate.challenge_invalid", "Only active or stale relations can be challenged")
		}
		return nil

	case model.DebateChallengeTargetReference:
		if debate.CurrentRevisionID == nil {
			return apperr.BadRequest("debate.challenge_invalid", "Debate has no current revision")
		}
		var row model.ContentReference
		if err := tx.First(&row, "id = ? AND source_type = ? AND source_id = ?", targetID, "debate_revision", *debate.CurrentRevisionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.BadRequest("debate.challenge_invalid", "Referenced citation not found on current revision")
			}
			return err
		}
		var relCount int64
		if err := tx.Model(&model.DebateRevisionReference{}).
			Where("revision_id = ? AND content_reference_id = ?", *debate.CurrentRevisionID, row.ID).
			Count(&relCount).Error; err != nil {
			return err
		}
		if relCount > 0 {
			return apperr.BadRequest("debate.challenge_invalid", "Debate relations must be challenged using target_type 'relation'")
		}
		var matchingRowIDs []uuid.UUID
		if err := tx.Model(&model.ContentReference{}).
			Where("target_type = ? AND target_id = ?", row.TargetType, row.TargetID).
			Pluck("id", &matchingRowIDs).Error; err != nil {
			return err
		}
		if len(matchingRowIDs) > 0 {
			var existingOpen int64
			if err := tx.Model(&model.DebateChallenge{}).
				Where("debate_id = ? AND target_type = ? AND target_id IN ? AND status = ?",
					debate.ID, model.DebateChallengeTargetReference, matchingRowIDs, model.DebateChallengeStatusOpen).
				Count(&existingOpen).Error; err != nil {
				return err
			}
			if existingOpen > 0 {
				return apperr.Conflict("debate.challenge_already_open", "An open challenge already exists for this target")
			}
			var existingUpheld int64
			if err := tx.Model(&model.DebateChallenge{}).
				Where("debate_id = ? AND target_type = ? AND target_id IN ? AND status = ?",
					debate.ID, model.DebateChallengeTargetReference, matchingRowIDs, model.DebateChallengeStatusUpheld).
				Count(&existingUpheld).Error; err != nil {
				return err
			}
			if existingUpheld > 0 {
				return apperr.BadRequest("debate.challenge_invalid", "This citation has already been rejected")
			}
		}
		return nil

	case model.DebateChallengeTargetEntry:
		var entry model.DebateEntry
		if err := tx.First(&entry, "id = ? AND debate_id = ?", targetID, debate.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.BadRequest("debate.challenge_invalid", "Evidence entry not found on this debate")
			}
			return err
		}
		if entry.EntryType != model.DebateEntryTypeEvidence || entry.Status != model.DebateEntryStatusActive {
			return apperr.BadRequest("debate.challenge_invalid", "Only active evidence entries can be challenged")
		}
		return nil
	}
	return apperr.BadRequest("debate.challenge_invalid", "invalid challenge target_type")
}

func (s *Service) recomputeAndEvaluateChallenge(tx *gorm.DB, challenge *model.DebateChallenge) error {
	type counts struct {
		UpholdVotes  int
		DismissVotes int
	}
	var count counts
	if err := tx.Model(&model.DebateChallengeVote{}).
		Where("challenge_id = ?", challenge.ID).
		Select(
			"COALESCE(SUM(CASE WHEN decision = ? THEN 1 ELSE 0 END), 0) AS uphold_votes, COALESCE(SUM(CASE WHEN decision = ? THEN 1 ELSE 0 END), 0) AS dismiss_votes",
			model.DebateChallengeVoteUphold, model.DebateChallengeVoteDismiss,
		).Scan(&count).Error; err != nil {
		return err
	}

	updates := map[string]any{
		"uphold_votes":  count.UpholdVotes,
		"dismiss_votes": count.DismissVotes,
	}
	total := count.UpholdVotes + count.DismissVotes
	settings := service.NewSiteAccessService(tx).LoadDebateSettings()

	if total >= settings.ChallengeMinVotes {
		if count.UpholdVotes*100 > total*settings.ConclusionThresholdPercent {
			now := tx.NowFunc()
			updates["status"] = model.DebateChallengeStatusUpheld
			updates["resolved_at"] = &now
			if err := tx.Model(challenge).Updates(updates).Error; err != nil {
				return err
			}
			challenge.UpholdVotes = count.UpholdVotes
			challenge.DismissVotes = count.DismissVotes
			challenge.Status = model.DebateChallengeStatusUpheld
			challenge.ResolvedAt = &now
			return applyChallengeUpheldEffect(tx, challenge)
		}
		if count.DismissVotes*100 > total*settings.ConclusionThresholdPercent {
			now := tx.NowFunc()
			updates["status"] = model.DebateChallengeStatusDismissed
			updates["resolved_at"] = &now
			return tx.Model(challenge).Updates(updates).Error
		}
	}

	return tx.Model(challenge).Updates(updates).Error
}

func applyChallengeUpheldEffect(tx *gorm.DB, challenge *model.DebateChallenge) error {
	switch challenge.TargetType {
	case model.DebateChallengeTargetEntry:
		return tx.Model(&model.DebateEntry{}).
			Where("id = ? AND debate_id = ?", challenge.TargetID, challenge.DebateID).
			Updates(map[string]any{
				"status":                model.DebateEntryStatusRejected,
				"rejected_challenge_id": challenge.ID,
			}).Error

	case model.DebateChallengeTargetRelation:
		if err := tx.Model(&model.DebateRelation{}).
			Where("id = ? AND target_debate_id = ?", challenge.TargetID, challenge.DebateID).
			Update("status", model.DebateRelationRejected).Error; err != nil {
			return err
		}
		var debate model.Debate
		if err := tx.Select("current_revision_id").First(&debate, "id = ?", challenge.DebateID).Error; err != nil {
			return err
		}
		if debate.CurrentRevisionID != nil {
			if err := tx.Model(&model.DebateRevisionReference{}).
				Where("revision_id = ? AND relation_id = ?", *debate.CurrentRevisionID, challenge.TargetID).
				Update("state", model.DebateRelationRejected).Error; err != nil {
				return err
			}
		}
		return nil

	default:
		return nil
	}
}

func (s *Service) loadSingleChallengeDTO(viewerID, debateID, challengeID uuid.UUID) (DebateChallengeDTO, error) {
	var challenge model.DebateChallenge
	if err := s.db.Preload("Creator").First(&challenge, "id = ? AND debate_id = ?", challengeID, debateID).Error; err != nil {
		return DebateChallengeDTO{}, err
	}
	dtos, err := s.challengeDTOs(viewerID, []model.DebateChallenge{challenge})
	if err != nil {
		return DebateChallengeDTO{}, err
	}
	if len(dtos) == 0 {
		return DebateChallengeDTO{}, gorm.ErrRecordNotFound
	}
	return dtos[0], nil
}

func (s *Service) challengeDTOs(viewerID uuid.UUID, challenges []model.DebateChallenge) ([]DebateChallengeDTO, error) {
	if len(challenges) == 0 {
		return []DebateChallengeDTO{}, nil
	}
	ids := make([]uuid.UUID, 0, len(challenges))
	relationIDs := make([]uuid.UUID, 0)
	referenceRowIDs := make([]uuid.UUID, 0)
	entryIDs := make([]uuid.UUID, 0)
	for _, ch := range challenges {
		ids = append(ids, ch.ID)
		switch ch.TargetType {
		case model.DebateChallengeTargetRelation:
			relationIDs = append(relationIDs, ch.TargetID)
		case model.DebateChallengeTargetReference:
			referenceRowIDs = append(referenceRowIDs, ch.TargetID)
		case model.DebateChallengeTargetEntry:
			entryIDs = append(entryIDs, ch.TargetID)
		}
	}

	refsByChallenge := map[uuid.UUID][]reference.ResolvedReference{}
	if model.HasTableCached(s.db, &model.ContentReference{}) {
		var rows []model.ContentReference
		if err := s.db.Where("source_type = ? AND source_id IN ? AND source_field = ?", "debate_challenge", ids, "rationale").
			Order("start_offset ASC, id ASC").Find(&rows).Error; err != nil {
			return nil, err
		}
		resolved, err := s.referenceService().ResolveStoredRows(s.db, reference.Viewer{}, rows)
		if err != nil {
			return nil, err
		}
		refsByChallenge = resolved
	}

	userVotes := map[uuid.UUID]string{}
	if viewerID != uuid.Nil && model.HasTableCached(s.db, &model.DebateChallengeVote{}) {
		var votes []model.DebateChallengeVote
		if err := s.db.Where("challenge_id IN ? AND user_id = ?", ids, viewerID).Find(&votes).Error; err != nil {
			return nil, err
		}
		for _, vote := range votes {
			userVotes[vote.ChallengeID] = vote.Decision
		}
	}

	relationLabels := map[uuid.UUID]string{}
	if len(relationIDs) > 0 {
		var relations []model.DebateRelation
		if err := s.db.Preload("SourceDebate").Where("id IN ?", relationIDs).Find(&relations).Error; err == nil {
			for _, rel := range relations {
				if rel.SourceDebate != nil && strings.TrimSpace(rel.SourceDebate.Title) != "" {
					relationLabels[rel.ID] = rel.SourceDebate.Title
				}
			}
		}
	}

	refLabels := map[uuid.UUID]string{}
	if len(referenceRowIDs) > 0 && model.HasTableCached(s.db, &model.ContentReference{}) {
		var rows []model.ContentReference
		if err := s.db.Where("id IN ?", referenceRowIDs).Find(&rows).Error; err == nil {
			registry := reference.NewRegistry(s.db)
			for _, row := range rows {
				if resolved, err := registry.Resolve(reference.Viewer{}, row.TargetType, row.TargetID); err == nil && strings.TrimSpace(resolved.Label) != "" {
					refLabels[row.ID] = resolved.Label
				} else {
					refLabels[row.ID] = "@" + row.TargetType + ":" + row.TargetID.String()
				}
			}
		}
	}

	entryPreviews := map[uuid.UUID]string{}
	if len(entryIDs) > 0 && model.HasTableCached(s.db, &model.DebateEntry{}) {
		var entries []model.DebateEntry
		if err := s.db.Unscoped().Where("id IN ?", entryIDs).Find(&entries).Error; err == nil {
			for _, entry := range entries {
				label := strings.TrimSpace(entry.Title)
				if label == "" {
					label = strings.TrimSpace(entry.Content)
				}
				runes := []rune(label)
				if len(runes) > 80 {
					label = string(runes[:80]) + "…"
				}
				entryPreviews[entry.ID] = label
			}
		}
	}

	settings := service.NewSiteAccessService(s.db).LoadDebateSettings()
	result := make([]DebateChallengeDTO, 0, len(challenges))
	for _, ch := range challenges {
		refs := refsByChallenge[ch.ID]
		if refs == nil {
			refs = []reference.ResolvedReference{}
		}
		targetLabel := ""
		targetPreview := ""
		switch ch.TargetType {
		case model.DebateChallengeTargetRelation:
			targetLabel = relationLabels[ch.TargetID]
		case model.DebateChallengeTargetReference:
			targetLabel = refLabels[ch.TargetID]
		case model.DebateChallengeTargetEntry:
			targetLabel = "结构化证据"
			targetPreview = entryPreviews[ch.TargetID]
		}
		result = append(result, DebateChallengeDTO{
			ID:               ch.ID,
			DebateID:         ch.DebateID,
			TargetType:       ch.TargetType,
			TargetID:         ch.TargetID,
			TargetLabel:      targetLabel,
			TargetPreview:    targetPreview,
			CreatedBy:        ch.CreatedBy,
			Creator:          model.PublicUserFromUser(ch.Creator),
			ReasonCode:       ch.ReasonCode,
			Rationale:        ch.Rationale,
			References:       refs,
			Status:           ch.Status,
			UpholdVotes:      ch.UpholdVotes,
			DismissVotes:     ch.DismissVotes,
			CurrentUserVote:  userVotes[ch.ID],
			MinVotes:         settings.ChallengeMinVotes,
			ThresholdPercent: settings.ConclusionThresholdPercent,
			ResolvedAt:       ch.ResolvedAt,
			CreatedAt:        ch.CreatedAt,
			UpdatedAt:        ch.UpdatedAt,
		})
	}
	return result, nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(strings.ToLower(msg), "duplicate key")
}
