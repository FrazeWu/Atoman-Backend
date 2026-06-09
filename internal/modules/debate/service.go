package debate

import (
	"strings"

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

func (s *Service) CreateArgument(user authctx.CurrentUser, req CreateArgumentRequest) (model.Argument, error) {
	if user.ID == uuid.Nil {
		return model.Argument{}, apperr.Unauthorized("Login required")
	}
	if req.DebateID == uuid.Nil {
		return model.Argument{}, apperr.BadRequest("validation.invalid_request", "debate_id is required")
	}
	content := strings.TrimSpace(req.Content)
	argumentType := strings.TrimSpace(req.ArgumentType)
	if content == "" || argumentType == "" {
		return model.Argument{}, apperr.BadRequest("validation.invalid_request", "content and argument_type are required")
	}
	argument := model.Argument{
		DebateID:     req.DebateID,
		UserID:       user.ID,
		Content:      content,
		ArgumentType: model.ArgumentType(argumentType),
	}
	if err := s.repo.CreateArgument(&argument); err != nil {
		return model.Argument{}, err
	}
	return argument, nil
}

