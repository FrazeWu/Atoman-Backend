package debate_voting

import (
	"errors"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type VoteStats struct {
	YesVotes         int    `json:"yes_votes"`
	NoVotes          int    `json:"no_votes"`
	TotalVotes       int    `json:"total_votes"`
	CurrentDirection string `json:"current_direction"`
	CurrentUserVote  string `json:"current_user_vote"`
}

type Service struct{ db *gorm.DB }

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) GetVotes(user authctx.CurrentUser, debateID uuid.UUID) (VoteStats, error) {
	var debate model.Debate
	if err := s.db.First(&debate, "id = ?", debateID).Error; err != nil {
		return VoteStats{}, debateNotFound(err)
	}
	return loadVoteStats(s.db, debate, user.ID)
}

func (s *Service) SetVote(user authctx.CurrentUser, debateID uuid.UUID, direction string) (VoteStats, error) {
	if err := requireActiveUser(s.db, user); err != nil {
		return VoteStats{}, err
	}
	if direction != model.DebateVoteYes && direction != model.DebateVoteNo {
		return VoteStats{}, apperr.BadRequest("validation.invalid_request", "direction must be yes or no")
	}
	var stats VoteStats
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var debate model.Debate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&debate, "id = ?", debateID).Error; err != nil {
			return debateNotFound(err)
		}
		var vote model.DebateVote
		result := tx.Unscoped().Where("debate_id = ? AND user_id = ?", debateID, user.ID).Limit(1).Find(&vote)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			vote = model.DebateVote{DebateID: debateID, UserID: user.ID, Direction: direction}
			if err := tx.Create(&vote).Error; err != nil {
				return err
			}
		} else if err := tx.Unscoped().Model(&vote).Updates(map[string]any{"direction": direction, "deleted_at": nil}).Error; err != nil {
			return err
		}
		loaded, err := loadVoteStats(tx, debate, user.ID)
		if err != nil {
			return err
		}
		if err := evaluateConclusion(tx, &debate, loaded); err != nil {
			return err
		}
		loaded.CurrentDirection = debate.ConclusionType
		stats = loaded
		return nil
	})
	return stats, err
}

func (s *Service) DeleteVote(user authctx.CurrentUser, debateID uuid.UUID) (VoteStats, error) {
	if err := requireActiveUser(s.db, user); err != nil {
		return VoteStats{}, err
	}
	var stats VoteStats
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var debate model.Debate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&debate, "id = ?", debateID).Error; err != nil {
			return debateNotFound(err)
		}
		var vote model.DebateVote
		if err := tx.Where("debate_id = ? AND user_id = ?", debateID, user.ID).First(&vote).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.vote_not_found", "Vote not found")
			}
			return err
		}
		if err := tx.Delete(&vote).Error; err != nil {
			return err
		}
		loaded, err := loadVoteStats(tx, debate, user.ID)
		if err != nil {
			return err
		}
		if err := evaluateConclusion(tx, &debate, loaded); err != nil {
			return err
		}
		loaded.CurrentDirection = debate.ConclusionType
		stats = loaded
		return nil
	})
	return stats, err
}

func (s *Service) ListConclusions(debateID uuid.UUID) ([]model.DebateConclusionEvent, error) {
	var count int64
	if err := s.db.Model(&model.Debate{}).Where("id = ?", debateID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, apperr.NotFound("debate.not_found", "Debate not found")
	}
	var events []model.DebateConclusionEvent
	err := s.db.Where("debate_id = ?", debateID).Order("created_at DESC, id DESC").Find(&events).Error
	return events, err
}

func loadVoteStats(db *gorm.DB, debate model.Debate, userID uuid.UUID) (VoteStats, error) {
	type counts struct {
		YesVotes int
		NoVotes  int
	}
	var count counts
	err := db.Model(&model.DebateVote{}).Where("debate_id = ?", debate.ID).Select(
		"SUM(CASE WHEN direction = ? THEN 1 ELSE 0 END) AS yes_votes, SUM(CASE WHEN direction = ? THEN 1 ELSE 0 END) AS no_votes",
		model.DebateVoteYes, model.DebateVoteNo,
	).Scan(&count).Error
	if err != nil {
		return VoteStats{}, err
	}
	stats := VoteStats{YesVotes: count.YesVotes, NoVotes: count.NoVotes, TotalVotes: count.YesVotes + count.NoVotes, CurrentDirection: debate.ConclusionType}
	if userID != uuid.Nil {
		var vote model.DebateVote
		result := db.Where("debate_id = ? AND user_id = ?", debate.ID, userID).Limit(1).Find(&vote)
		if result.Error != nil {
			return VoteStats{}, result.Error
		}
		if result.RowsAffected > 0 {
			stats.CurrentUserVote = vote.Direction
		}
	}
	return stats, nil
}

func evaluateConclusion(tx *gorm.DB, debate *model.Debate, stats VoteStats) error {
	candidate := qualifyingDirection(stats)
	if candidate == "" || candidate == debate.ConclusionType {
		return nil
	}
	if debate.ConclusionType != "" && candidate == debate.ConclusionType {
		return nil
	}
	if debate.ConclusionType != "" {
		opposite := model.DebateVoteNo
		if debate.ConclusionType == model.DebateVoteNo {
			opposite = model.DebateVoteYes
		}
		if candidate != opposite {
			return nil
		}
	}
	oldEventID := uuid.Nil
	if debate.CurrentConclusionEventID != nil {
		oldEventID = *debate.CurrentConclusionEventID
	}
	event := model.DebateConclusionEvent{
		DebateID: debate.ID, Direction: candidate, YesVotes: stats.YesVotes,
		NoVotes: stats.NoVotes, TotalVotes: stats.TotalVotes,
	}
	if err := tx.Create(&event).Error; err != nil {
		return err
	}
	if err := tx.Model(debate).Updates(map[string]any{"conclusion_type": candidate, "current_conclusion_event_id": event.ID}).Error; err != nil {
		return err
	}
	debate.ConclusionType, debate.CurrentConclusionEventID = candidate, &event.ID
	if oldEventID != uuid.Nil {
		if err := tx.Model(&model.DebateRelation{}).
			Where("source_debate_id = ? AND source_conclusion_event_id = ? AND status = ?", debate.ID, oldEventID, model.DebateRelationActive).
			Update("status", model.DebateRelationStale).Error; err != nil {
			return err
		}
	}
	return nil
}

func qualifyingDirection(stats VoteStats) string {
	if stats.TotalVotes < 10 {
		return ""
	}
	if stats.YesVotes*4 > stats.TotalVotes*3 {
		return model.DebateVoteYes
	}
	if stats.NoVotes*4 > stats.TotalVotes*3 {
		return model.DebateVoteNo
	}
	return ""
}

func requireActiveUser(db *gorm.DB, user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	var count int64
	if err := db.Model(&model.User{}).Where("uuid = ? AND is_active = ?", user.ID, true).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return apperr.Forbidden("auth.inactive", "Account is inactive")
	}
	return nil
}

func debateNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("debate.not_found", "Debate not found")
	}
	return err
}
