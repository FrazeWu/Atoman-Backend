package forum

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Service struct {
	db   *gorm.DB
	repo *Repo
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db, repo: NewRepo(db)}
}

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
	topics, total, err := s.repo.ListTopics(query)
	if err != nil {
		return nil, 0, err
	}
	if err := s.applyCommentState(topics); err != nil {
		return nil, 0, err
	}
	return topics, total, nil
}

func (s *Service) GetTopic(id uuid.UUID) (model.ForumTopic, error) {
	if id == uuid.Nil {
		return model.ForumTopic{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	topic, err := s.repo.GetTopic(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ForumTopic{}, apperr.NotFound("forum.topic_not_found", "Forum topic not found")
	}
	if err != nil {
		return model.ForumTopic{}, err
	}
	topics := []model.ForumTopic{topic}
	if err := s.applyCommentState(topics); err != nil {
		return model.ForumTopic{}, err
	}
	return topics[0], nil
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

func (s *Service) applyCommentState(topics []model.ForumTopic) error {
	if len(topics) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(topics))
	for i := range topics {
		ids[i] = topics[i].ID
		topics[i].ReplyCount = 0
		topics[i].LastReplyAt = nil
		topics[i].IsSolved = false
		topics[i].SolvedReplyID = nil
	}
	var targets []model.DiscussionTarget
	if err := s.db.Where("kind = ? AND resource_id IN ?", comment.TargetKindForumTopic, ids).Find(&targets).Error; err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	targetByResource := make(map[uuid.UUID]model.DiscussionTarget, len(targets))
	targetIDs := make([]uuid.UUID, len(targets))
	for i, target := range targets {
		targetByResource[target.ResourceID] = target
		targetIDs[i] = target.ID
	}
	visibleStatuses := []string{comment.CommentStatusActive, comment.CommentStatusAutoFolded}
	latestIDs := s.db.Table("comment_entries AS candidate").
		Select("candidate.id").
		Where("candidate.target_id IN ? AND candidate.deleted_at IS NULL AND candidate.status IN ?", targetIDs, visibleStatuses).
		Where(`candidate.root_id IS NULL OR EXISTS (
			SELECT 1 FROM comment_entries AS roots
			WHERE roots.id = candidate.root_id AND roots.deleted_at IS NULL AND roots.status IN ?
		)`, visibleStatuses).
		Where(`candidate.id = (
			SELECT latest.id FROM comment_entries AS latest
			WHERE latest.target_id = candidate.target_id
			  AND latest.deleted_at IS NULL AND latest.status IN ?
			  AND (latest.root_id IS NULL OR EXISTS (
				SELECT 1 FROM comment_entries AS latest_roots
				WHERE latest_roots.id = latest.root_id AND latest_roots.deleted_at IS NULL AND latest_roots.status IN ?
			  ))
			ORDER BY latest.created_at DESC, latest.id DESC LIMIT 1
		)`, visibleStatuses, visibleStatuses)
	var latest []model.CommentEntry
	if err := s.db.Where("id IN (?)", latestIDs).Find(&latest).Error; err != nil {
		return err
	}
	latestByTarget := make(map[uuid.UUID]*time.Time, len(latest))
	for _, item := range latest {
		if _, exists := latestByTarget[item.TargetID]; !exists {
			createdAt := item.CreatedAt
			latestByTarget[item.TargetID] = &createdAt
		}
	}
	for i := range topics {
		target, ok := targetByResource[topics[i].ID]
		if !ok {
			continue
		}
		topics[i].ReplyCount = target.CommentCount
		topics[i].LastReplyAt = latestByTarget[target.ID]
		topics[i].SolvedReplyID = target.PinnedCommentID
		topics[i].IsSolved = target.PinnedCommentID != nil
	}
	return nil
}
