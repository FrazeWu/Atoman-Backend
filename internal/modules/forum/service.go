package forum

import (
	"errors"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/modules/forum_moderation"
	"atoman/internal/modules/reference"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	coreservice "atoman/internal/service"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct {
	db         *gorm.DB
	repo       *Repo
	trust      *coreservice.ForumTrustService
	references *reference.Service
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db, repo: NewRepo(db), trust: coreservice.NewForumTrustService(db), references: reference.NewService(db)}
}

func (s *Service) syncTopicReferences(tx *gorm.DB, topic model.ForumTopic) ([]reference.ResolvedReference, error) {
	var permissionCount int64
	if err := tx.Model(&model.ForumCategoryPermission{}).Where("category_id = ?", topic.CategoryID).Count(&permissionCount).Error; err != nil {
		return nil, err
	}
	audience := ""
	if permissionCount == 0 {
		audience = reference.AudiencePublic
	}
	return s.references.ReplacePublished(tx, reference.Source{
		Type: "thread", ID: topic.ID, ActorID: topic.UserID, Audience: audience,
		Meta: map[string]interface{}{"module": "forum", "path": "/topic/" + topic.ID.String()},
	}, []reference.Field{{Name: "content", Content: topic.Content}})
}

func (s *Service) topicDTOs(db *gorm.DB, topics []model.ForumTopic, user authctx.CurrentUser) ([]TopicDTO, error) {
	dtos := make([]TopicDTO, len(topics))
	ids := make([]uuid.UUID, 0, len(topics))
	for index, topic := range topics {
		dtos[index] = TopicDTO{ForumTopic: topic, References: []reference.ResolvedReference{}}
		ids = append(ids, topic.ID)
	}
	if len(ids) == 0 {
		return dtos, nil
	}
	if err := s.applyTopicCapabilities(db, dtos, user); err != nil {
		return nil, err
	}
	var rows []model.ContentReference
	if err := db.Where("source_type = ? AND source_id IN ?", "thread", ids).
		Order("source_id ASC, source_field ASC, start_offset ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	resolved, err := s.references.ResolveStoredRows(db, reference.Viewer{UserID: user.ID}, rows)
	if err != nil {
		return nil, err
	}
	for index := range dtos {
		if items, ok := resolved[dtos[index].ID]; ok {
			dtos[index].References = items
		}
	}
	return dtos, nil
}

func (s *Service) applyTopicCapabilities(db *gorm.DB, topics []TopicDTO, user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return nil
	}
	categoryIDs := make([]uuid.UUID, 0, len(topics))
	seenCategories := make(map[uuid.UUID]struct{}, len(topics))
	for index := range topics {
		topics[index].CanEditTopic = topics[index].UserID == user.ID || authctx.RoleAtLeast(user.Role, authctx.RoleModerator)
		if _, exists := seenCategories[topics[index].CategoryID]; !exists {
			seenCategories[topics[index].CategoryID] = struct{}{}
			categoryIDs = append(categoryIDs, topics[index].CategoryID)
		}
	}
	if authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		for index := range topics {
			topics[index].CanPinTopic = true
			topics[index].CanLockTopic = true
		}
		return nil
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return nil
	}
	var assignments []model.ForumModeratorAssignment
	if err := db.Where("user_id = ? AND (category_id IS NULL OR category_id IN ?)", user.ID, categoryIDs).Find(&assignments).Error; err != nil {
		return err
	}
	for index := range topics {
		for _, assignment := range assignments {
			if assignment.CategoryID == nil || *assignment.CategoryID == topics[index].CategoryID {
				topics[index].CanPinTopic = topics[index].CanPinTopic || assignment.CanPinTopic
				topics[index].CanLockTopic = topics[index].CanLockTopic || assignment.CanLockTopic
			}
		}
	}
	return nil
}

func (s *Service) topicDTO(db *gorm.DB, topic model.ForumTopic, user authctx.CurrentUser) (TopicDTO, error) {
	items, err := s.topicDTOs(db, []model.ForumTopic{topic}, user)
	if err != nil {
		return TopicDTO{}, err
	}
	return items[0], nil
}

func (s *Service) ListCategories(user authctx.CurrentUser) ([]model.ForumCategory, error) {
	return s.repo.ListCategories(user)
}

func (s *Service) GetCategory(user authctx.CurrentUser, id uuid.UUID) (model.ForumCategory, error) {
	if id == uuid.Nil {
		return model.ForumCategory{}, apperr.BadRequest("validation.invalid_request", "category_id is required")
	}
	category, err := s.repo.GetCategory(user, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ForumCategory{}, apperr.NotFound("forum.category_not_found", "Forum category not found")
	}
	return category, err
}

func (s *Service) ListTopics(user authctx.CurrentUser, query ListTopicsQuery) ([]model.ForumTopic, int64, error) {
	topics, total, err := s.repo.ListTopics(user, query)
	if err != nil {
		return nil, 0, err
	}
	if err := s.applyTopicState(topics, user); err != nil {
		return nil, 0, err
	}
	return topics, total, nil
}

func (s *Service) SearchTopics(user authctx.CurrentUser, query ListTopicsQuery) ([]model.ForumTopic, int64, error) {
	topics, total, err := s.repo.SearchTopics(user, query)
	if err != nil {
		return nil, 0, err
	}
	if err := s.applyTopicState(topics, user); err != nil {
		return nil, 0, err
	}
	return topics, total, nil
}

func (s *Service) GetTopic(user authctx.CurrentUser, id uuid.UUID) (model.ForumTopic, error) {
	if id == uuid.Nil {
		return model.ForumTopic{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	topic, err := s.repo.GetTopic(user, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ForumTopic{}, apperr.NotFound("forum.topic_not_found", "Forum topic not found")
	}
	if err != nil {
		return model.ForumTopic{}, err
	}
	topics := []model.ForumTopic{topic}
	if err := s.applyTopicState(topics, user); err != nil {
		return model.ForumTopic{}, err
	}
	return topics[0], nil
}

func lockForumUser(tx *gorm.DB, userID uuid.UUID) error {
	var user model.User
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("uuid").First(&user, "uuid = ?", userID).Error
}

func (s *Service) evaluateTrustAfterWrite(userID uuid.UUID) {
	if _, err := s.trust.Evaluate(userID); err != nil {
		log.Printf("forum trust evaluation failed for user %s: %v", userID, err)
	}
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
	if err := s.CanCreateTopic(user, req.CategoryID); err != nil {
		return model.ForumTopic{}, err
	}
	if silenced, err := forum_moderation.IsUserSilenced(s.db, user.ID, time.Now().UTC()); err != nil {
		return model.ForumTopic{}, err
	} else if silenced {
		return model.ForumTopic{}, apperr.Forbidden("forum.user_silenced", "You are temporarily silenced")
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
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockForumUser(tx, user.ID); err != nil {
			return err
		}
		if err := s.trust.WithDB(tx).CheckCreateTopic(user, title, content); err != nil {
			return err
		}
		if err := NewRepo(tx).CreateTopic(&topic); err != nil {
			return err
		}
		_, err := s.syncTopicReferences(tx, topic)
		return err
	})
	if err != nil {
		return model.ForumTopic{}, err
	}
	if err := s.notifyTopicFollowers(topic); err != nil {
		log.Printf("forum follow notification failed for topic %s: %v", topic.ID, err)
	}
	s.evaluateTrustAfterWrite(user.ID)
	return s.repo.GetTopic(user, topic.ID)
}

func (s *Service) notifyTopicFollowers(topic model.ForumTopic) error {
	query := s.db.Model(&model.ForumFollow{}).
		Where("target_type = ? AND target_key = ?", model.ForumFollowTargetCategory, topic.CategoryID.String())
	if len(topic.Tags) > 0 {
		query = query.Or("target_type = ? AND target_key IN ?", model.ForumFollowTargetTag, []string(topic.Tags))
	}
	var follows []model.ForumFollow
	if err := query.Find(&follows).Error; err != nil {
		return err
	}
	matchedByUser := make(map[uuid.UUID][]string)
	for _, follow := range follows {
		if follow.UserID == topic.UserID {
			continue
		}
		target := follow.TargetType + ":" + follow.TargetKey
		matchedByUser[follow.UserID] = append(matchedByUser[follow.UserID], target)
	}
	candidateIDs := make([]uuid.UUID, 0, len(matchedByUser))
	for userID := range matchedByUser {
		candidateIDs = append(candidateIDs, userID)
	}
	allowedIDs, err := s.repo.FilterUsersWhoCanViewCategory(candidateIDs, topic.CategoryID)
	if err != nil {
		return err
	}
	actorID := topic.UserID
	var notificationErrors []error
	for _, recipientID := range allowedIDs {
		matchedTargets := matchedByUser[recipientID]
		notification := model.Notification{
			RecipientID: recipientID,
			ActorID:     &actorID,
			Type:        "forum_follow",
			SourceType:  "forum_topic",
			SourceID:    topic.ID,
			Meta: model.NotificationMeta{
				"topic_id":        topic.ID.String(),
				"topic_title":     topic.Title,
				"matched_targets": matchedTargets,
			},
		}
		if err := s.db.Create(&notification).Error; err != nil {
			notificationErrors = append(notificationErrors, err)
		}
	}
	return errors.Join(notificationErrors...)
}

func (s *Service) UpdateTopic(user authctx.CurrentUser, topicID uuid.UUID, req UpdateTopicRequest) (model.ForumTopic, error) {
	topic, err := s.GetTopic(user, topicID)
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
	var tags *model.StringSlice
	if req.Tags != nil {
		normalized, err := normalizeTags(*req.Tags)
		if err != nil {
			return model.ForumTopic{}, err
		}
		tags = &normalized
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockForumUser(tx, topic.UserID); err != nil {
			return err
		}
		locked, err := NewRepo(tx).GetTopicForUpdate(topicID)
		if err != nil {
			return err
		}
		if err := requireTopicOwner(user, locked.UserID); err != nil {
			return err
		}
		if err := s.trust.WithDB(tx).CheckUpdateTopic(locked.UserID, locked.ID, title, content); err != nil {
			return err
		}
		locked.Title = title
		locked.Content = content
		if tags != nil {
			locked.Tags = *tags
		}
		if err := NewRepo(tx).SaveTopic(&locked); err != nil {
			return err
		}
		_, err = s.syncTopicReferences(tx, locked)
		return err
	})
	if err != nil {
		return model.ForumTopic{}, err
	}
	return s.repo.GetTopic(user, topic.ID)
}

func (s *Service) DeleteTopic(user authctx.CurrentUser, topicID uuid.UUID) error {
	topic, err := s.GetTopic(user, topicID)
	if err != nil {
		return err
	}
	if err := requireTopicOwner(user, topic.UserID); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.references.RemoveSource(tx, "thread", topicID); err != nil {
			return err
		}
		return NewRepo(tx).DeleteTopic(topicID)
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
	key, err := s.normalizeFollowTarget(user, targetType, targetKey, true)
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
	key, err := s.normalizeFollowTarget(user, targetType, targetKey, false)
	if err != nil {
		return err
	}
	return s.repo.DeleteFollow(user.ID, targetType, key)
}

func (s *Service) ListFollowerIDs(targetType, targetKey string) ([]uuid.UUID, error) {
	key, err := s.normalizeFollowTarget(authctx.CurrentUser{Role: authctx.RoleAnonymous}, targetType, targetKey, false)
	if err != nil {
		return nil, err
	}
	return s.repo.ListFollowerIDs(targetType, key)
}

func (s *Service) normalizeFollowTarget(user authctx.CurrentUser, targetType, targetKey string, requireExists bool) (string, error) {
	switch targetType {
	case model.ForumFollowTargetTopic:
		id, err := uuid.Parse(targetKey)
		if err != nil {
			return "", apperr.BadRequest("validation.invalid_request", "targetKey must be a valid uuid")
		}
		if requireExists {
			if _, err := s.GetTopic(user, id); err != nil {
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
			if _, err := s.GetCategory(user, id); err != nil {
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
			exists, err := s.repo.TagExists(user, key)
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

func (s *Service) applyTopicState(topics []model.ForumTopic, user authctx.CurrentUser) error {
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
		topics[i].IsLiked = false
		topics[i].IsBookmarked = false
	}
	if user.ID != uuid.Nil {
		var likes []model.ForumLike
		if err := s.db.Where("user_id = ? AND target_type = ? AND target_id IN ?", user.ID, "topic", ids).Find(&likes).Error; err != nil {
			return err
		}
		likedIDs := make(map[uuid.UUID]struct{}, len(likes))
		for _, like := range likes {
			likedIDs[like.TargetID] = struct{}{}
		}
		var bookmarks []model.ForumBookmark
		if err := s.db.Where("user_id = ? AND topic_id IN ?", user.ID, ids).Find(&bookmarks).Error; err != nil {
			return err
		}
		bookmarkedIDs := make(map[uuid.UUID]struct{}, len(bookmarks))
		for _, bookmark := range bookmarks {
			bookmarkedIDs[bookmark.TopicID] = struct{}{}
		}
		for i := range topics {
			_, topics[i].IsLiked = likedIDs[topics[i].ID]
			_, topics[i].IsBookmarked = bookmarkedIDs[topics[i].ID]
		}
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
