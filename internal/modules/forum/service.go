package forum

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Service struct {
	db       *gorm.DB
	repo     *Repo
	comments *comment.Service
}

func NewService(db *gorm.DB, services ...*comment.Service) *Service {
	commentService := comment.NewService(db, comment.NewTargetRegistry(db))
	if len(services) > 0 && services[0] != nil {
		commentService = services[0]
	}
	return &Service{db: db, repo: NewRepo(db), comments: commentService}
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

	topic := model.ForumTopic{
		UserID:     user.ID,
		CategoryID: req.CategoryID,
		Title:      title,
		Content:    content,
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

func (s *Service) ListReplies(topicID uuid.UUID) (comment.CommentListDTO, error) {
	if _, err := s.GetTopic(topicID); err != nil {
		return comment.CommentListDTO{}, err
	}
	return s.comments.List(authctx.CurrentUser{}, forumTopicTarget(topicID), comment.ListCommentsInput{Page: 1, PageSize: 20, Sort: comment.SortOldest})
}

func (s *Service) CreateReply(user authctx.CurrentUser, req CreateReplyRequest) (comment.CommentDTO, error) {
	if user.ID == uuid.Nil {
		return comment.CommentDTO{}, apperr.Unauthorized("Login required")
	}
	if req.TopicID == uuid.Nil {
		return comment.CommentDTO{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	if _, err := s.GetTopic(req.TopicID); err != nil {
		return comment.CommentDTO{}, err
	}
	return s.comments.Create(user, forumTopicTarget(req.TopicID), comment.CreateCommentInput{
		Content:   req.Content,
		ReplyToID: req.ParentReplyID,
	})
}

func (s *Service) UpdateReply(user authctx.CurrentUser, replyID uuid.UUID, req UpdateReplyRequest) (comment.CommentDTO, error) {
	return s.comments.Edit(user, replyID, comment.EditCommentInput{Content: req.Content})
}

func (s *Service) DeleteReply(user authctx.CurrentUser, replyID uuid.UUID) error {
	return s.comments.Delete(user, replyID)
}

func (s *Service) ListDrafts(user authctx.CurrentUser) ([]model.ForumDraft, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	return s.repo.ListDrafts(user.ID)
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

func forumTopicTarget(topicID uuid.UUID) comment.TargetRef {
	return comment.TargetRef{Kind: comment.TargetKindForumTopic, ResourceID: topicID}
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
	var latest []model.CommentEntry
	if err := s.db.Model(&model.CommentEntry{}).
		Select("target_id", "created_at").
		Where("target_id IN ? AND status IN ?", targetIDs, []string{comment.CommentStatusActive, comment.CommentStatusAutoFolded}).
		Order("created_at DESC, id DESC").Find(&latest).Error; err != nil {
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
