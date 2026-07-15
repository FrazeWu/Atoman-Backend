package forum

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

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

func (s *Service) ListCategories() ([]model.ForumCategory, error) {
	return s.repo.ListCategories()
}

func (s *Service) GetCategory(id uuid.UUID) (model.ForumCategory, error) {
	if id == uuid.Nil {
		return model.ForumCategory{}, apperr.BadRequest("validation.invalid_request", "category_id is required")
	}
	category, err := s.repo.GetCategory(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ForumCategory{}, apperr.NotFound("forum.category_not_found", "Forum category not found")
	}
	return category, err
}

func (s *Service) ListTopics(query ListTopicsQuery) ([]model.ForumTopic, int64, error) {
	return s.repo.ListTopics(query)
}

func (s *Service) GetTopic(id uuid.UUID) (model.ForumTopic, error) {
	if id == uuid.Nil {
		return model.ForumTopic{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	topic, err := s.repo.GetTopic(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ForumTopic{}, apperr.NotFound("forum.topic_not_found", "Forum topic not found")
	}
	return topic, err
}

func (s *Service) CreateTopic(user authctx.CurrentUser, req CreateTopicRequest) (model.ForumTopic, error) {
	if user.ID == uuid.Nil {
		return model.ForumTopic{}, apperr.Unauthorized("Login required")
	}
	if req.CategoryID == uuid.Nil {
		return model.ForumTopic{}, apperr.BadRequest("validation.invalid_request", "category_id is required")
	}
	title := strings.TrimSpace(req.Title)
	content := strings.TrimSpace(req.Content)
	if title == "" || content == "" {
		return model.ForumTopic{}, apperr.BadRequest("validation.invalid_request", "title and content are required")
	}
	if _, err := s.GetCategory(req.CategoryID); err != nil {
		return model.ForumTopic{}, err
	}
	tags, err := normalizeTags(req.Tags)
	if err != nil {
		return model.ForumTopic{}, err
	}

	topic := model.ForumTopic{
		UserID:     user.ID,
		CategoryID: req.CategoryID,
		Title:      title,
		Content:    content,
		Tags:       tags,
	}
	if err := s.repo.CreateTopic(&topic); err != nil {
		return model.ForumTopic{}, err
	}
	return s.repo.GetTopic(topic.ID)
}

func (s *Service) UpdateTopic(user authctx.CurrentUser, topicID uuid.UUID, req UpdateTopicRequest) (model.ForumTopic, error) {
	topic, err := s.GetTopic(topicID)
	if err != nil {
		return model.ForumTopic{}, err
	}
	if err := requireTopicOwner(user, topic.UserID); err != nil {
		return model.ForumTopic{}, err
	}
	title := strings.TrimSpace(req.Title)
	content := strings.TrimSpace(req.Content)
	if title == "" || content == "" {
		return model.ForumTopic{}, apperr.BadRequest("validation.invalid_request", "title and content are required")
	}
	topic.Title = title
	topic.Content = content
	if req.Tags != nil {
		tags, err := normalizeTags(*req.Tags)
		if err != nil {
			return model.ForumTopic{}, err
		}
		topic.Tags = tags
	}
	if err := s.repo.SaveTopic(&topic); err != nil {
		return model.ForumTopic{}, err
	}
	return s.repo.GetTopic(topic.ID)
}

func (s *Service) DeleteTopic(user authctx.CurrentUser, topicID uuid.UUID) error {
	topic, err := s.GetTopic(topicID)
	if err != nil {
		return err
	}
	if err := requireTopicOwner(user, topic.UserID); err != nil {
		return err
	}
	return s.repo.DeleteTopic(topicID)
}

func (s *Service) ListReplies(topicID uuid.UUID, sort string) ([]model.ForumReply, error) {
	if _, err := s.GetTopic(topicID); err != nil {
		return nil, err
	}
	return s.repo.ListReplies(topicID, sort)
}

func (s *Service) CreateReply(user authctx.CurrentUser, req CreateReplyRequest) (model.ForumReply, error) {
	if user.ID == uuid.Nil {
		return model.ForumReply{}, apperr.Unauthorized("Login required")
	}
	if req.TopicID == uuid.Nil {
		return model.ForumReply{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return model.ForumReply{}, apperr.BadRequest("validation.invalid_request", "content is required")
	}

	var createdID uuid.UUID
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		topic, err := repo.GetTopicForUpdate(req.TopicID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("forum.topic_not_found", "Forum topic not found")
			}
			return err
		}
		count, err := repo.CountReplies(topic.ID)
		if err != nil {
			return err
		}
		reply := model.ForumReply{
			TopicID:       topic.ID,
			UserID:        user.ID,
			ParentReplyID: req.ParentReplyID,
			Content:       content,
			FloorNumber:   int(count) + 1,
			Depth:         0,
		}
		if req.ParentReplyID != nil && *req.ParentReplyID != uuid.Nil {
			parent, err := repo.GetReply(*req.ParentReplyID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return apperr.NotFound("forum.reply_not_found", "Forum reply not found")
				}
				return err
			}
			if parent.TopicID != topic.ID {
				return apperr.BadRequest("validation.invalid_request", "parent reply must belong to the same topic")
			}
			reply.Depth = parent.Depth + 1
		}
		if err := repo.CreateReply(&reply); err != nil {
			return err
		}
		now := time.Now().UTC()
		topic.ReplyCount = int(count) + 1
		topic.LastReplyAt = &now
		if err := repo.SaveTopic(&topic); err != nil {
			return err
		}
		createdID = reply.ID
		return nil
	})
	if err != nil {
		return model.ForumReply{}, err
	}
	return s.repo.GetReply(createdID)
}

func (s *Service) GetReply(id uuid.UUID) (model.ForumReply, error) {
	if id == uuid.Nil {
		return model.ForumReply{}, apperr.BadRequest("validation.invalid_request", "reply_id is required")
	}
	reply, err := s.repo.GetReply(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ForumReply{}, apperr.NotFound("forum.reply_not_found", "Forum reply not found")
	}
	return reply, err
}

func (s *Service) UpdateReply(user authctx.CurrentUser, replyID uuid.UUID, req UpdateReplyRequest) (model.ForumReply, error) {
	reply, err := s.GetReply(replyID)
	if err != nil {
		return model.ForumReply{}, err
	}
	if err := requireTopicOwner(user, reply.UserID); err != nil {
		return model.ForumReply{}, err
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return model.ForumReply{}, apperr.BadRequest("validation.invalid_request", "content is required")
	}
	reply.Content = content
	if err := s.repo.SaveReply(&reply); err != nil {
		return model.ForumReply{}, err
	}
	return s.repo.GetReply(reply.ID)
}

func (s *Service) DeleteReply(user authctx.CurrentUser, replyID uuid.UUID) error {
	reply, err := s.GetReply(replyID)
	if err != nil {
		return err
	}
	if err := requireTopicOwner(user, reply.UserID); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		if err := repo.DeleteReply(replyID); err != nil {
			return err
		}
		return s.recalculateTopicReplyState(tx, reply.TopicID)
	})
}

func (s *Service) ListDrafts(user authctx.CurrentUser) ([]model.ForumDraft, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	return s.repo.ListDrafts(user.ID)
}

func (s *Service) GetDraft(user authctx.CurrentUser, contextKey string) (*model.ForumDraft, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	contextKey = strings.TrimSpace(contextKey)
	if contextKey == "" {
		return nil, apperr.BadRequest("validation.invalid_request", "context_key is required")
	}
	draft, err := s.repo.GetDraft(user.ID, contextKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &draft, nil
}

func (s *Service) SaveDraft(user authctx.CurrentUser, req SaveDraftRequest) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	contextKey := strings.TrimSpace(req.ContextKey)
	if contextKey == "" {
		return apperr.BadRequest("validation.invalid_request", "context_key is required")
	}
	draft := model.ForumDraft{
		UserID:     user.ID,
		ContextKey: contextKey,
		Title:      strings.TrimSpace(req.Title),
		Content:    strings.TrimSpace(req.Content),
		Tags:       strings.TrimSpace(req.Tags),
	}
	return s.repo.UpsertDraft(&draft)
}

func (s *Service) DeleteDraft(user authctx.CurrentUser, draftID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if draftID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "draft_id is required")
	}
	return s.repo.DeleteDraft(user.ID, draftID)
}

func (s *Service) DeleteDraftByContext(user authctx.CurrentUser, contextKey string) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	contextKey = strings.TrimSpace(contextKey)
	if contextKey == "" {
		return apperr.BadRequest("validation.invalid_request", "context_key is required")
	}
	return s.repo.DeleteDraftByContext(user.ID, contextKey)
}

func (s *Service) Follow(user authctx.CurrentUser, targetType, targetKey string) (model.ForumFollow, error) {
	if user.ID == uuid.Nil {
		return model.ForumFollow{}, apperr.Unauthorized("Login required")
	}
	key, err := s.normalizeFollowTarget(targetType, targetKey, true)
	if err != nil {
		return model.ForumFollow{}, err
	}
	follow := model.ForumFollow{UserID: user.ID, TargetType: targetType, TargetKey: key}
	if err := s.repo.UpsertFollow(&follow); err != nil {
		return model.ForumFollow{}, err
	}
	return follow, nil
}

func (s *Service) ListFollows(user authctx.CurrentUser) ([]model.ForumFollow, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	return s.repo.ListFollows(user.ID)
}

func (s *Service) Unfollow(user authctx.CurrentUser, targetType, targetKey string) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	key, err := s.normalizeFollowTarget(targetType, targetKey, false)
	if err != nil {
		return err
	}
	return s.repo.DeleteFollow(user.ID, targetType, key)
}

func (s *Service) ListFollowerIDs(targetType, targetKey string) ([]uuid.UUID, error) {
	key, err := s.normalizeFollowTarget(targetType, targetKey, false)
	if err != nil {
		return nil, err
	}
	return s.repo.ListFollowerIDs(targetType, key)
}

func (s *Service) normalizeFollowTarget(targetType, targetKey string, requireExists bool) (string, error) {
	switch targetType {
	case model.ForumFollowTargetTopic:
		id, err := uuid.Parse(targetKey)
		if err != nil {
			return "", apperr.BadRequest("validation.invalid_request", "targetKey must be a valid uuid")
		}
		if requireExists {
			if _, err := s.GetTopic(id); err != nil {
				return "", err
			}
		}
		return id.String(), nil
	case model.ForumFollowTargetCategory:
		id, err := uuid.Parse(targetKey)
		if err != nil {
			return "", apperr.BadRequest("validation.invalid_request", "targetKey must be a valid uuid")
		}
		if requireExists {
			if _, err := s.GetCategory(id); err != nil {
				return "", err
			}
		}
		return id.String(), nil
	case model.ForumFollowTargetTag:
		key := strings.TrimSpace(targetKey)
		if key == "" || utf8.RuneCountInString(key) > 30 {
			return "", apperr.BadRequest("validation.invalid_request", "tag must be 1 to 30 characters")
		}
		if requireExists {
			exists, err := s.repo.TagExists(key)
			if err != nil {
				return "", err
			}
			if !exists {
				return "", apperr.NotFound("forum.tag_not_found", "Forum tag not found")
			}
		}
		return key, nil
	default:
		return "", apperr.BadRequest("validation.invalid_request", "targetType must be topic, category, or tag")
	}
}

func (s *Service) CreateCategoryRequest(user authctx.CurrentUser, req CreateCategoryRequestRequest) (model.CategoryRequest, error) {
	if user.ID == uuid.Nil {
		return model.CategoryRequest{}, apperr.Unauthorized("Login required")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return model.CategoryRequest{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	request := model.CategoryRequest{
		UserID:      user.ID,
		Name:        name,
		Description: strings.TrimSpace(req.Description),
		Reason:      strings.TrimSpace(req.Reason),
		Status:      "pending",
	}
	if err := s.db.Create(&request).Error; err != nil {
		return model.CategoryRequest{}, err
	}
	return request, nil
}

func requireTopicOwner(user authctx.CurrentUser, ownerID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if user.ID != ownerID && !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return apperr.Forbidden("forum.forbidden", "You do not have permission to modify this resource")
	}
	return nil
}

func normalizeTags(raw []string) (model.StringSlice, error) {
	tags := make(model.StringSlice, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		tag := strings.TrimSpace(value)
		if tag == "" {
			continue
		}
		if utf8.RuneCountInString(tag) > 30 {
			return nil, apperr.BadRequest("validation.invalid_request", "each tag must be at most 30 characters")
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) > 5 {
		return nil, apperr.BadRequest("validation.invalid_request", "at most 5 tags are allowed")
	}
	return tags, nil
}

func (s *Service) recalculateTopicReplyState(tx *gorm.DB, topicID uuid.UUID) error {
	var replyCount int64
	if err := tx.Model(&model.ForumReply{}).Where("topic_id = ?", topicID).Count(&replyCount).Error; err != nil {
		return err
	}

	updates := map[string]any{
		"reply_count": int(replyCount),
	}

	if replyCount == 0 {
		updates["last_reply_at"] = nil
		updates["is_solved"] = false
		updates["solved_reply_id"] = nil
		return tx.Model(&model.ForumTopic{}).Where("id = ?", topicID).Updates(updates).Error
	}

	var latestReply model.ForumReply
	if err := tx.Select("id", "created_at").Where("topic_id = ?", topicID).Order("created_at DESC, id DESC").First(&latestReply).Error; err != nil {
		return err
	}
	lastReplyAt := latestReply.CreatedAt
	updates["last_reply_at"] = &lastReplyAt

	var solvedReply model.ForumReply
	if err := tx.Select("id").Where("topic_id = ? AND is_solved = ?", topicID, true).Order("created_at DESC, id DESC").First(&solvedReply).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			updates["is_solved"] = false
			updates["solved_reply_id"] = nil
		} else {
			return err
		}
	} else {
		solvedReplyID := solvedReply.ID
		updates["is_solved"] = true
		updates["solved_reply_id"] = &solvedReplyID
	}

	return tx.Model(&model.ForumTopic{}).Where("id = ?", topicID).Updates(updates).Error
}
