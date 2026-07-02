package debate

import (
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Service struct {
	db   *gorm.DB
	repo *Repo
}

func NewService(db *gorm.DB) *Service { return &Service{db: db, repo: NewRepo(db)} }

func (s *Service) ListDebates(query ListDebatesQuery) ([]model.Debate, int64, error) {
	return s.repo.ListDebates(query)
}

func (s *Service) GetDebate(id uuid.UUID) (model.Debate, error) {
	if id == uuid.Nil {
		return model.Debate{}, apperr.BadRequest("validation.invalid_request", "debate_id is required")
	}
	debate, err := s.repo.GetDebate(id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.Debate{}, apperr.NotFound("debate.not_found", "Debate not found")
		}
		return model.Debate{}, err
	}
	return debate, nil
}

func (s *Service) CreateDebate(user authctx.CurrentUser, req CreateDebateRequest) (model.Debate, error) {
	if user.ID == uuid.Nil {
		return model.Debate{}, apperr.Unauthorized("Login required")
	}

	title := strings.TrimSpace(req.Title)
	description := strings.TrimSpace(req.Description)
	if title == "" {
		return model.Debate{}, apperr.BadRequest("validation.invalid_request", "title is required")
	}

	debate := model.Debate{
		UserID:      user.ID,
		Title:       title,
		Description: description,
		Status:      "open",
		ConcludeThreshold: 10,
	}
	if err := s.repo.CreateDebate(&debate); err != nil {
		return model.Debate{}, err
	}
	return s.repo.GetDebate(debate.ID)
}

func (s *Service) UpdateDebate(user authctx.CurrentUser, debateID uuid.UUID, req CreateDebateRequest) (model.Debate, error) {
	debate, err := s.GetDebate(debateID)
	if err != nil {
		return model.Debate{}, err
	}
	if debate.UserID != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return model.Debate{}, apperr.Forbidden("debate.forbidden", "Not authorized")
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		return model.Debate{}, apperr.BadRequest("validation.invalid_request", "title is required")
	}
	debate.Title = title
	debate.Description = strings.TrimSpace(req.Description)
	debate.Content = strings.TrimSpace(req.Content)
	debate.Tags = req.Tags
	if err := s.repo.SaveDebate(&debate); err != nil {
		return model.Debate{}, err
	}
	return s.repo.GetDebate(debate.ID)
}

func (s *Service) DeleteDebate(user authctx.CurrentUser, debateID uuid.UUID) error {
	debate, err := s.GetDebate(debateID)
	if err != nil {
		return err
	}
	if debate.UserID != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("debate.forbidden", "Not authorized")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("debate_id = ?", debateID).Delete(&model.Argument{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Debate{}, "id = ?", debateID).Error
	})
}

func (s *Service) ConcludeDebate(user authctx.CurrentUser, debateID uuid.UUID, conclusionType string, conclusionSummary string) (model.Debate, error) {
	debate, err := s.GetDebate(debateID)
	if err != nil {
		return model.Debate{}, err
	}
	if debate.UserID != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return model.Debate{}, apperr.Forbidden("debate.forbidden", "Not authorized")
	}
	switch strings.TrimSpace(conclusionType) {
	case "yes", "no", "inconclusive":
	default:
		return model.Debate{}, apperr.BadRequest("validation.invalid_request", "conclusion_type is invalid")
	}
	debate.Status = "concluded"
	now := time.Now()
	debate.ConcludedAt = &now
	debate.ConclusionType = strings.TrimSpace(conclusionType)
	debate.ConclusionSummary = strings.TrimSpace(conclusionSummary)
	if err := s.repo.SaveDebate(&debate); err != nil {
		return model.Debate{}, err
	}
	return s.repo.GetDebate(debate.ID)
}

func (s *Service) ReopenDebate(user authctx.CurrentUser, debateID uuid.UUID) (model.Debate, error) {
	debate, err := s.GetDebate(debateID)
	if err != nil {
		return model.Debate{}, err
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return model.Debate{}, apperr.Forbidden("debate.forbidden", "Admin only")
	}
	debate.Status = "open"
	debate.ConcludedAt = nil
	debate.ConclusionType = ""
	debate.ConclusionSummary = ""
	if err := s.repo.SaveDebate(&debate); err != nil {
		return model.Debate{}, err
	}
	return s.repo.GetDebate(debate.ID)
}

func (s *Service) CreateArgument(user authctx.CurrentUser, req CreateArgumentRequest) (model.Argument, error) {
	if user.ID == uuid.Nil {
		return model.Argument{}, apperr.Unauthorized("Login required")
	}
	if req.DebateID == uuid.Nil {
		return model.Argument{}, apperr.BadRequest("validation.invalid_request", "debate_id is required")
	}
	debate, err := s.GetDebate(req.DebateID)
	if err != nil {
		return model.Argument{}, err
	}
	if debate.Status != "open" {
		return model.Argument{}, apperr.BadRequest("debate.closed", "Debate is closed")
	}
	content := strings.TrimSpace(req.Content)
	argumentType := strings.TrimSpace(req.ArgumentType)
	if content == "" || argumentType == "" {
		return model.Argument{}, apperr.BadRequest("validation.invalid_request", "content and argument_type are required")
	}
	argument := model.Argument{
		DebateID:     req.DebateID,
		ParentID:     req.ParentID,
		UserID:       user.ID,
		Content:      content,
		ArgumentType: model.ArgumentType(argumentType),
		SourceURL:    strings.TrimSpace(req.SourceURL),
		SourceTitle:  strings.TrimSpace(req.SourceTitle),
		SourceExcerpt: strings.TrimSpace(req.SourceExcerpt),
	}
	if err := s.repo.CreateArgument(&argument); err != nil {
		return model.Argument{}, err
	}
	_ = s.db.Model(&model.Debate{}).Where("id = ?", req.DebateID).
		UpdateColumn("argument_count", gorm.Expr("argument_count + 1")).Error
	return s.repo.GetArgument(argument.ID)
}

func (s *Service) UpdateArgument(user authctx.CurrentUser, argumentID uuid.UUID, req CreateArgumentRequest) (model.Argument, error) {
	argument, err := s.repo.GetArgument(argumentID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.Argument{}, apperr.NotFound("debate.argument_not_found", "Argument not found")
		}
		return model.Argument{}, err
	}
	if argument.UserID != user.ID {
		return model.Argument{}, apperr.Forbidden("debate.forbidden", "Not authorized")
	}
	content := strings.TrimSpace(req.Content)
	argumentType := strings.TrimSpace(req.ArgumentType)
	if content == "" || argumentType == "" {
		return model.Argument{}, apperr.BadRequest("validation.invalid_request", "content and argument_type are required")
	}
	argument.Content = content
	argument.ArgumentType = model.ArgumentType(argumentType)
	argument.SourceURL = strings.TrimSpace(req.SourceURL)
	argument.SourceTitle = strings.TrimSpace(req.SourceTitle)
	argument.SourceExcerpt = strings.TrimSpace(req.SourceExcerpt)
	if err := s.repo.SaveArgument(&argument); err != nil {
		return model.Argument{}, err
	}
	return s.repo.GetArgument(argument.ID)
}

func (s *Service) DeleteArgument(user authctx.CurrentUser, argumentID uuid.UUID) error {
	argument, err := s.repo.GetArgument(argumentID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperr.NotFound("debate.argument_not_found", "Argument not found")
		}
		return err
	}
	if argument.UserID != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("debate.forbidden", "Not authorized")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Argument{}).Where("parent_id = ?", argument.ID).Update("parent_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Where("argument_id = ?", argument.ID).Delete(&model.DebateVote{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&model.Argument{}, "id = ?", argument.ID).Error; err != nil {
			return err
		}
		return tx.Model(&model.Debate{}).Where("id = ?", argument.DebateID).
			UpdateColumn("argument_count", gorm.Expr("CASE WHEN argument_count > 0 THEN argument_count - 1 ELSE 0 END")).Error
	})
}

func (s *Service) AddArgumentReference(user authctx.CurrentUser, argumentID uuid.UUID, referenceID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	argument, err := s.repo.GetArgument(argumentID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperr.NotFound("debate.argument_not_found", "Argument not found")
		}
		return err
	}
	refArgument, err := s.repo.GetArgument(referenceID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperr.NotFound("debate.reference_not_found", "Reference argument not found")
		}
		return err
	}
	return s.db.Model(&argument).Association("References").Append(&refArgument)
}

func (s *Service) RemoveArgumentReference(user authctx.CurrentUser, argumentID uuid.UUID, referenceID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	argument, err := s.repo.GetArgument(argumentID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperr.NotFound("debate.argument_not_found", "Argument not found")
		}
		return err
	}
	refArgument, err := s.repo.GetArgument(referenceID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperr.NotFound("debate.reference_not_found", "Reference not found")
		}
		return err
	}
	return s.db.Model(&argument).Association("References").Delete(&refArgument)
}

func (s *Service) AddDebateReference(user authctx.CurrentUser, argumentID uuid.UUID, debateID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	argument, err := s.repo.GetArgument(argumentID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperr.NotFound("debate.argument_not_found", "Argument not found")
		}
		return err
	}
	debate, err := s.repo.GetDebate(debateID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperr.NotFound("debate.not_found", "Debate not found")
		}
		return err
	}
	return s.db.Model(&argument).Association("ReferencedDebates").Append(&debate)
}

func (s *Service) RemoveDebateReference(user authctx.CurrentUser, argumentID uuid.UUID, debateID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	argument, err := s.repo.GetArgument(argumentID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperr.NotFound("debate.argument_not_found", "Argument not found")
		}
		return err
	}
	debate, err := s.repo.GetDebate(debateID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperr.NotFound("debate.not_found", "Debate not found")
		}
		return err
	}
	return s.db.Model(&argument).Association("ReferencedDebates").Delete(&debate)
}

func (s *Service) FoldArgument(user authctx.CurrentUser, argumentID uuid.UUID, foldNote string) error {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("debate.forbidden", "Admin only")
	}
	return s.db.Model(&model.Argument{}).Where("id = ?", argumentID).Updates(map[string]any{
		"is_folded": true,
		"fold_note": strings.TrimSpace(foldNote),
	}).Error
}

func (s *Service) UnfoldArgument(user authctx.CurrentUser, argumentID uuid.UUID) error {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("debate.forbidden", "Admin only")
	}
	return s.db.Model(&model.Argument{}).Where("id = ?", argumentID).Updates(map[string]any{
		"is_folded": false,
		"fold_note": "",
	}).Error
}

func (s *Service) ListArguments(debateID uuid.UUID) ([]model.Argument, error) {
	if debateID == uuid.Nil {
		return nil, apperr.BadRequest("validation.invalid_request", "debate_id is required")
	}
	if _, err := s.GetDebate(debateID); err != nil {
		return nil, err
	}
	return s.repo.ListArguments(debateID)
}
