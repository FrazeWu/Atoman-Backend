package debate_voting

import (
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ConclusionVoteState struct {
	ConcludeVoteCount int  `json:"conclude_vote_count"`
	ConcludeThreshold int  `json:"conclude_threshold"`
	AutoConcluded     bool `json:"auto_concluded"`
}

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

func (s *Service) SetDebateVote(user authctx.CurrentUser, debateID uuid.UUID, voteType int) (model.DebateVote, error) {
	return model.DebateVote{}, apperr.BadRequest("debate.vote_unsupported", "debate votes require a debate_id column on debate_votes")
}

func (s *Service) RemoveDebateVote(user authctx.CurrentUser, debateID uuid.UUID) error {
	return apperr.BadRequest("debate.vote_unsupported", "debate votes require a debate_id column on debate_votes")
}

func (s *Service) SetArgumentVote(user authctx.CurrentUser, argumentID uuid.UUID, voteType int) (model.DebateVote, error) {
	if user.ID == uuid.Nil {
		return model.DebateVote{}, apperr.Unauthorized("Login required")
	}
	if argumentID == uuid.Nil {
		return model.DebateVote{}, apperr.BadRequest("validation.invalid_request", "argument_id is required")
	}
	if voteType != 1 && voteType != -1 {
		return model.DebateVote{}, apperr.BadRequest("validation.invalid_request", "vote_type must be 1 or -1")
	}

	var saved model.DebateVote
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var detail model.DebateArgumentDetail
		if err := tx.First(&detail, "comment_id = ?", argumentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.argument_not_found", "Argument not found")
			}
			return err
		}

		var existing model.DebateVote
		result := tx.Where("argument_id = ? AND user_id = ?", argumentID, user.ID).Limit(1).Find(&existing)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if existing.VoteType == voteType {
				saved = existing
				return nil
			}
			oldVoteType := existing.VoteType
			if err := tx.Model(&existing).Update("vote_type", voteType).Error; err != nil {
				return err
			}
			history := model.VoteHistory{
				ArgumentID:  argumentID,
				UserID:      user.ID,
				OldVoteType: oldVoteType,
				NewVoteType: voteType,
			}
			if err := tx.Create(&history).Error; err != nil {
				return err
			}
			existing.VoteType = voteType
			saved = existing
			return nil
		}

		vote := model.DebateVote{
			ArgumentID: argumentID,
			UserID:     user.ID,
			VoteType:   voteType,
		}
		if err := tx.Create(&vote).Error; err != nil {
			return err
		}
		saved = vote
		return nil
	})
	if err != nil {
		return model.DebateVote{}, err
	}
	return saved, nil
}

func (s *Service) RemoveArgumentVote(user authctx.CurrentUser, argumentID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if argumentID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "argument_id is required")
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var vote model.DebateVote
		if err := tx.Where("argument_id = ? AND user_id = ?", argumentID, user.ID).First(&vote).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.vote_not_found", "Vote not found")
			}
			return err
		}
		if err := tx.Delete(&vote).Error; err != nil {
			return err
		}
		return nil
	})
}

func (s *Service) SetConclusionVote(user authctx.CurrentUser, debateID uuid.UUID) (ConclusionVoteState, error) {
	if user.ID == uuid.Nil {
		return ConclusionVoteState{}, apperr.Unauthorized("Login required")
	}
	if debateID == uuid.Nil {
		return ConclusionVoteState{}, apperr.BadRequest("validation.invalid_request", "debate_id is required")
	}

	var state ConclusionVoteState
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var debate model.Debate
		if err := tx.First(&debate, "id = ?", debateID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.not_found", "Debate not found")
			}
			return err
		}
		if debate.Status != "open" {
			return apperr.BadRequest("debate.not_open", "Debate is not open")
		}

		var existing model.DebateConcludeVote
		result := tx.Where("debate_id = ? AND user_id = ?", debateID, user.ID).Limit(1).Find(&existing)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			vote := model.DebateConcludeVote{DebateID: debateID, UserID: user.ID}
			if err := tx.Create(&vote).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.Debate{}).Where("id = ?", debateID).UpdateColumn("conclude_vote_count", gorm.Expr("conclude_vote_count + 1")).Error; err != nil {
				return err
			}
		}

		if err := tx.First(&debate, "id = ?", debateID).Error; err != nil {
			return err
		}
		if debate.ConcludeVoteCount >= debate.ConcludeThreshold {
			now := time.Now()
			if err := tx.Model(&model.Debate{}).Where("id = ?", debateID).Updates(map[string]any{
				"status":          "concluded",
				"concluded_at":    now,
				"conclusion_type": "inconclusive",
			}).Error; err != nil {
				return err
			}
			debate.Status = "concluded"
		}

		state = ConclusionVoteState{
			ConcludeVoteCount: debate.ConcludeVoteCount,
			ConcludeThreshold: debate.ConcludeThreshold,
			AutoConcluded:     debate.Status == "concluded",
		}
		return nil
	})
	if err != nil {
		return ConclusionVoteState{}, err
	}
	return state, nil
}

func (s *Service) RemoveConclusionVote(user authctx.CurrentUser, debateID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if debateID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "debate_id is required")
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var vote model.DebateConcludeVote
		result := tx.Where("debate_id = ? AND user_id = ?", debateID, user.ID).Limit(1).Find(&vote)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperr.NotFound("debate.conclusion_vote_not_found", "Conclusion vote not found")
		}
		if err := tx.Delete(&vote).Error; err != nil {
			return err
		}
		return tx.Model(&model.Debate{}).Where("id = ?", debateID).UpdateColumn("conclude_vote_count", gorm.Expr("conclude_vote_count - 1")).Error
	})
}
