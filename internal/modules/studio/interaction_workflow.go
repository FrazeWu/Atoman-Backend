package studio

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) interactionStates(channelID uuid.UUID, comments []model.CommentEntry) (map[uuid.UUID]model.StudioInteractionState, error) {
	statesByComment := make(map[uuid.UUID]model.StudioInteractionState, len(comments))
	if len(comments) == 0 {
		return statesByComment, nil
	}
	commentIDs := make([]uuid.UUID, 0, len(comments))
	for _, comment := range comments {
		commentIDs = append(commentIDs, comment.ID)
	}
	var states []model.StudioInteractionState
	if err := s.db.Where("channel_id = ? AND comment_id IN ?", channelID, commentIDs).Find(&states).Error; err != nil {
		return nil, err
	}
	for _, state := range states {
		statesByComment[state.CommentID] = state
	}
	return statesByComment, nil
}

func (s *Service) UpdateInteractionState(user authctx.CurrentUser, module Module, channelID, commentID uuid.UUID, input UpdateInteractionStateInput) error {
	if input.Handled == nil && input.Priority == nil {
		return apperr.BadRequest("studio.invalid_interaction_state", "handled or priority is required")
	}
	return s.updateInteractionStates(user, module, channelID, []uuid.UUID{commentID}, input)
}

func (s *Service) SetInteractionsHandled(user authctx.CurrentUser, module Module, channelID uuid.UUID, commentIDs []uuid.UUID, handled bool) error {
	return s.updateInteractionStates(user, module, channelID, commentIDs, UpdateInteractionStateInput{Handled: &handled})
}

func (s *Service) updateInteractionStates(user authctx.CurrentUser, module Module, channelID uuid.UUID, commentIDs []uuid.UUID, input UpdateInteractionStateInput) error {
	if err := requireUser(user); err != nil {
		return err
	}
	if _, err := ParseModule(string(module)); err != nil {
		return err
	}
	commentIDs = uniqueUUIDs(commentIDs)
	if len(commentIDs) == 0 {
		return apperr.BadRequest("studio.invalid_interactions", "comment_ids is required")
	}
	if len(commentIDs) > 100 {
		return apperr.BadRequest("studio.invalid_interactions", "at most 100 comments can be updated at once")
	}
	channel, err := s.resolveContentChannel(user.ID, channelID)
	if err != nil {
		return err
	}
	if err := s.validateInteractionComments(user.ID, channel.ID, module, commentIDs); err != nil {
		return err
	}
	if input.Priority != nil && *input.Priority != "normal" && *input.Priority != "high" {
		return apperr.BadRequest("studio.invalid_interaction_priority", "priority must be normal or high")
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var existing []model.StudioInteractionState
		if err := tx.Where("channel_id = ? AND comment_id IN ?", channel.ID, commentIDs).Find(&existing).Error; err != nil {
			return err
		}
		existingByComment := make(map[uuid.UUID]model.StudioInteractionState, len(existing))
		for _, state := range existing {
			existingByComment[state.CommentID] = state
		}
		now := time.Now().UTC()
		states := make([]model.StudioInteractionState, 0, len(commentIDs))
		for _, commentID := range commentIDs {
			state, exists := existingByComment[commentID]
			if !exists {
				state = model.StudioInteractionState{
					ChannelID: channel.ID, CommentID: commentID, Priority: "normal", CreatedAt: now,
				}
			}
			if input.Handled != nil {
				state.Handled = *input.Handled
				if state.Handled {
					state.HandledBy = &user.ID
					state.HandledAt = &now
				} else {
					state.HandledBy = nil
					state.HandledAt = nil
				}
			}
			if input.Priority != nil {
				state.Priority = *input.Priority
			}
			if state.Priority == "" {
				state.Priority = "normal"
			}
			state.UpdatedAt = now
			states = append(states, state)
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "channel_id"}, {Name: "comment_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"handled", "priority", "handled_by", "handled_at", "updated_at",
			}),
		}).Create(&states).Error
	})
}

func (s *Service) validateInteractionComments(userID, channelID uuid.UUID, module Module, commentIDs []uuid.UUID) error {
	targetKind, titles, err := s.interactionContentTitles(userID, channelID, module)
	if err != nil {
		return err
	}
	if len(titles) == 0 {
		return apperr.NotFound("studio.interaction_not_found", "interaction not found")
	}
	contentIDs := make([]uuid.UUID, 0, len(titles))
	for id := range titles {
		contentIDs = append(contentIDs, id)
	}
	var targetIDs []uuid.UUID
	if err := s.db.Model(&model.DiscussionTarget{}).
		Where("kind = ? AND resource_id IN ?", targetKind, contentIDs).
		Pluck("id", &targetIDs).Error; err != nil {
		return err
	}
	if len(targetIDs) == 0 {
		return apperr.NotFound("studio.interaction_not_found", "interaction not found")
	}
	var count int64
	if err := s.db.Model(&model.CommentEntry{}).
		Where("id IN ? AND target_id IN ? AND root_id IS NULL AND status = ?", commentIDs, targetIDs, "active").
		Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(commentIDs)) {
		return apperr.NotFound("studio.interaction_not_found", "interaction not found")
	}
	return nil
}

func (s *Service) ListReplyTemplates(user authctx.CurrentUser, channelID uuid.UUID) ([]StudioReplyTemplate, error) {
	if err := requireUser(user); err != nil {
		return nil, err
	}
	if _, err := s.ownedChannel(user.ID, channelID); err != nil {
		return nil, err
	}
	var templates []model.StudioReplyTemplate
	if err := s.db.Where("channel_id = ?", channelID).Order("updated_at DESC, id DESC").Find(&templates).Error; err != nil {
		return nil, err
	}
	result := make([]StudioReplyTemplate, 0, len(templates))
	for _, template := range templates {
		result = append(result, studioReplyTemplate(template))
	}
	return result, nil
}

func (s *Service) CreateReplyTemplate(user authctx.CurrentUser, input StudioReplyTemplateInput) (StudioReplyTemplate, error) {
	if err := requireUser(user); err != nil {
		return StudioReplyTemplate{}, err
	}
	if _, err := s.ownedChannel(user.ID, input.ChannelID); err != nil {
		return StudioReplyTemplate{}, err
	}
	name, content := strings.TrimSpace(input.Name), strings.TrimSpace(input.Content)
	if name == "" || content == "" {
		return StudioReplyTemplate{}, apperr.BadRequest("studio.invalid_reply_template", "name and content are required")
	}
	template := model.StudioReplyTemplate{ChannelID: input.ChannelID, CreatedBy: user.ID, Name: name, Content: content}
	if err := s.db.Create(&template).Error; err != nil {
		return StudioReplyTemplate{}, err
	}
	return studioReplyTemplate(template), nil
}

func (s *Service) DeleteReplyTemplate(user authctx.CurrentUser, id uuid.UUID) error {
	if err := requireUser(user); err != nil {
		return err
	}
	var template model.StudioReplyTemplate
	if err := s.db.First(&template, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("studio.reply_template_not_found", "reply template not found")
		}
		return err
	}
	if _, err := s.ownedChannel(user.ID, template.ChannelID); err != nil {
		return err
	}
	return s.db.Delete(&template).Error
}

func studioReplyTemplate(template model.StudioReplyTemplate) StudioReplyTemplate {
	return StudioReplyTemplate{
		ID: template.ID, ChannelID: template.ChannelID, Name: template.Name, Content: template.Content,
		CreatedAt: template.CreatedAt, UpdatedAt: template.UpdatedAt,
	}
}
