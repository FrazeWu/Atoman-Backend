package debate

import (
	"net/url"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct {
	db       *gorm.DB
	repo     *Repo
	comments *comment.Service
}

var validArgumentTypes = map[string]bool{"support": true, "oppose": true, "neutral": true, "evidence": true, "question": true, "counter": true}

func validateArgumentMetadata(argumentType, sourceURL string) error {
	if !validArgumentTypes[argumentType] {
		return apperr.BadRequest("debate.invalid_argument_type", "Invalid argument type")
	}
	if sourceURL == "" {
		return nil
	}
	parsed, err := url.Parse(sourceURL)
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return apperr.BadRequest("debate.invalid_source_url", "Source URL must be HTTP or HTTPS")
	}
	return nil
}

func NewService(db *gorm.DB, services ...*comment.Service) *Service {
	commentService := comment.NewService(db, comment.NewTargetRegistry(db))
	if len(services) > 0 && services[0] != nil {
		commentService = services[0]
	}
	return &Service{db: db, repo: NewRepo(db), comments: commentService}
}

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
		UserID:            user.ID,
		Title:             title,
		Description:       description,
		Status:            "open",
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
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var locked model.Debate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", debateID).Error; err != nil {
			return err
		}
		return tx.Model(&locked).Updates(map[string]any{"title": title, "description": strings.TrimSpace(req.Description), "content": strings.TrimSpace(req.Content), "tags": req.Tags}).Error
	})
	if err != nil {
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
	return s.comments.DeleteTarget(comment.TargetRef{Kind: comment.TargetKindDebate, ResourceID: debateID}, func(tx *gorm.DB) error {
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
	now := time.Now()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var locked model.Debate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", debateID).Error; err != nil {
			return err
		}
		return tx.Model(&locked).Updates(map[string]any{"status": "concluded", "concluded_at": now, "conclusion_type": strings.TrimSpace(conclusionType), "conclusion_summary": strings.TrimSpace(conclusionSummary)}).Error
	})
	if err != nil {
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
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var locked model.Debate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", debateID).Error; err != nil {
			return err
		}
		return tx.Model(&locked).Updates(map[string]any{"status": "open", "concluded_at": nil, "conclusion_type": "", "conclusion_summary": ""}).Error
	})
	if err != nil {
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
	content := req.Content
	argumentType := strings.TrimSpace(req.ArgumentType)
	if err := validateArgumentMetadata(argumentType, strings.TrimSpace(req.SourceURL)); err != nil {
		return model.Argument{}, err
	}
	if req.ParentID != nil {
		parent, err := s.repo.GetArgument(*req.ParentID)
		if err != nil || parent.DebateID != req.DebateID {
			return model.Argument{}, apperr.BadRequest("debate.invalid_parent", "Parent argument is invalid")
		}
	}
	created, err := s.comments.CreateWithExtension(user,
		comment.TargetRef{Kind: comment.TargetKindDebate, ResourceID: req.DebateID},
		comment.CreateCommentInput{Content: content, ReplyToID: req.ParentID, Mentions: req.Mentions, AttachmentIDs: req.AttachmentIDs},
		func(tx *gorm.DB, entry *model.CommentEntry) error {
			var current model.Debate
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", req.DebateID).Error; err != nil {
				return err
			}
			if current.Status != "open" {
				return apperr.BadRequest("debate.closed", "Debate is closed")
			}
			detail := model.DebateArgumentDetail{
				CommentID: entry.ID, ArgumentType: argumentType,
				SourceURL: strings.TrimSpace(req.SourceURL), SourceTitle: strings.TrimSpace(req.SourceTitle),
				SourceExcerpt: strings.TrimSpace(req.SourceExcerpt),
			}
			if err := tx.Create(&detail).Error; err != nil {
				return err
			}
			return tx.Model(&model.Debate{}).Where("id = ?", req.DebateID).
				UpdateColumn("argument_count", gorm.Expr("argument_count + 1")).Error
		})
	if err != nil {
		return model.Argument{}, err
	}
	return s.repo.GetArgument(created.ID)
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
	content := req.Content
	argumentType := strings.TrimSpace(req.ArgumentType)
	if err := validateArgumentMetadata(argumentType, strings.TrimSpace(req.SourceURL)); err != nil {
		return model.Argument{}, err
	}
	if _, err := s.comments.EditWithExtension(user, argumentID, comment.EditCommentInput{Content: content, Mentions: req.Mentions, AttachmentIDs: req.AttachmentIDs}, func(tx *gorm.DB, _ *model.CommentEntry) error {
		result := tx.Model(&model.DebateArgumentDetail{}).Where("comment_id = ?", argumentID).Updates(map[string]any{
			"argument_type": argumentType, "source_url": strings.TrimSpace(req.SourceURL),
			"source_title": strings.TrimSpace(req.SourceTitle), "source_excerpt": strings.TrimSpace(req.SourceExcerpt),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	}); err != nil {
		return model.Argument{}, err
	}
	return s.repo.GetArgument(argument.ID)
}

func (s *Service) GetArgument(argumentID uuid.UUID) (model.Argument, error) {
	argument, err := s.repo.GetArgument(argumentID)
	if err == gorm.ErrRecordNotFound {
		return model.Argument{}, apperr.NotFound("debate.argument_not_found", "Argument not found")
	}
	return argument, err
}

func (s *Service) DeleteArgument(user authctx.CurrentUser, argumentID uuid.UUID) error {
	_, err := s.repo.GetArgument(argumentID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperr.NotFound("debate.argument_not_found", "Argument not found")
		}
		return err
	}
	return s.comments.Delete(user, argumentID)
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
	if argument.DebateID != refArgument.DebateID {
		return apperr.BadRequest("debate.invalid_reference", "Reference must belong to the same debate")
	}
	return s.db.Create(&model.DebateArgumentReference{CommentID: argumentID, ReferencedCommentID: referenceID}).Error
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
	return s.db.Where("comment_id = ? AND referenced_comment_id = ?", argument.ID, refArgument.ID).Delete(&model.DebateArgumentReference{}).Error
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
	return s.db.Create(&model.DebateArgumentDebateRef{CommentID: argument.ID, DebateID: debate.ID}).Error
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
	return s.db.Where("comment_id = ? AND debate_id = ?", argument.ID, debate.ID).Delete(&model.DebateArgumentDebateRef{}).Error
}

func (s *Service) FoldArgument(user authctx.CurrentUser, argumentID uuid.UUID, foldNote string) error {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("debate.forbidden", "Admin only")
	}
	return s.db.Model(&model.DebateArgumentDetail{}).Where("comment_id = ?", argumentID).Updates(map[string]any{
		"is_folded": true,
		"fold_note": strings.TrimSpace(foldNote),
	}).Error
}

func (s *Service) UnfoldArgument(user authctx.CurrentUser, argumentID uuid.UUID) error {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("debate.forbidden", "Admin only")
	}
	return s.db.Model(&model.DebateArgumentDetail{}).Where("comment_id = ?", argumentID).Updates(map[string]any{
		"is_folded": false,
		"fold_note": "",
	}).Error
}

func (s *Service) ListArguments(debateID uuid.UUID, pageAndSize ...int) ([]model.Argument, int64, error) {
	if debateID == uuid.Nil {
		return nil, 0, apperr.BadRequest("validation.invalid_request", "debate_id is required")
	}
	if _, err := s.GetDebate(debateID); err != nil {
		return nil, 0, err
	}
	page, pageSize := 1, 20
	if len(pageAndSize) > 0 && pageAndSize[0] > 0 {
		page = pageAndSize[0]
	}
	if len(pageAndSize) > 1 && pageAndSize[1] > 0 {
		pageSize = pageAndSize[1]
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return s.repo.ListArguments(debateID, page, pageSize)
}
