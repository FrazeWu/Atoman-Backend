package forum_moderation

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ListReportsQuery struct {
	Page     int    `json:"page" form:"page"`
	PageSize int    `json:"page_size" form:"page_size"`
	Status   string `json:"status" form:"status"`
}

type ResolveReportRequest struct {
	ReviewNote string `json:"review_note"`
}

type CreateReportRequest struct {
	TargetType string    `json:"target_type"`
	TargetID   uuid.UUID `json:"target_id"`
	Reason     string    `json:"reason"`
	Note       string    `json:"note"`
}

type ReviewCategoryRequestInput struct {
	ReviewNote string `json:"review_note"`
	Color      string `json:"color"`
}

type ModeratorAssignmentInput struct {
	UserID                   uuid.UUID  `json:"user_id"`
	CategoryID               *uuid.UUID `json:"category_id"`
	CanReviewCategoryRequest bool       `json:"can_review_category_request"`
	CanPinTopic              bool       `json:"can_pin_topic"`
	CanLockTopic             bool       `json:"can_lock_topic"`
}

type UserActionRequest struct {
	Action        string `json:"action"`
	Reason        string `json:"reason"`
	DurationHours int    `json:"duration_hours"`
}

type ListUserActionsQuery struct {
	Page     int
	PageSize int
}

type ModerationUser struct {
	UUID        uuid.UUID `json:"uuid"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url"`
	Role        string    `json:"role"`
	IsActive    bool      `json:"is_active"`
}

func (s *Service) ListUsers(user authctx.CurrentUser, search string, page, pageSize int) ([]ModerationUser, int64, error) {
	if err := s.requireAdminOrOwner(user); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	q := s.db.Model(&model.User{})
	if term := strings.TrimSpace(search); term != "" {
		like := "%" + term + "%"
		q = q.Where("LOWER(username) LIKE LOWER(?) OR LOWER(display_name) LIKE LOWER(?)", like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var users []ModerationUser
	if err := q.Select("uuid, username, display_name, avatar_url, role, is_active").Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&users).Error; err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

func findModerationTargetForUpdate(db *gorm.DB, userID uuid.UUID, target *model.User) *gorm.DB {
	return db.Clauses(clause.Locking{Strength: "UPDATE"}).First(target, "uuid = ?", userID)
}

func IsUserSilenced(db *gorm.DB, userID uuid.UUID, now time.Time) (bool, error) {
	var latest model.ForumUserModerationAction
	err := db.Where("user_id = ? AND action IN ?", userID, []string{"silence", "unsilence"}).
		Order("created_at DESC, id DESC").First(&latest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return latest.Action == "silence" && latest.ExpiresAt != nil && latest.ExpiresAt.After(now), nil
}

func (s *Service) ListUserActions(user authctx.CurrentUser, userID uuid.UUID, query ListUserActionsQuery) ([]model.ForumUserModerationAction, int64, error) {
	if err := s.requireAdminOrOwner(user); err != nil {
		return nil, 0, err
	}
	if userID == uuid.Nil {
		return nil, 0, apperr.BadRequest("validation.invalid_request", "user_id is required")
	}
	page, pageSize := query.Page, query.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	q := s.db.Model(&model.ForumUserModerationAction{}).Where("user_id = ?", userID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var actions []model.ForumUserModerationAction
	if err := q.Order("created_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&actions).Error; err != nil {
		return nil, 0, err
	}
	return actions, total, nil
}

func (s *Service) ApplyUserAction(actor authctx.CurrentUser, userID uuid.UUID, req UserActionRequest) (model.ForumUserModerationAction, error) {
	if err := s.requireAdminOrOwner(actor); err != nil {
		return model.ForumUserModerationAction{}, err
	}
	if userID == uuid.Nil {
		return model.ForumUserModerationAction{}, apperr.BadRequest("validation.invalid_request", "user_id is required")
	}
	if actor.ID == userID {
		return model.ForumUserModerationAction{}, apperr.Forbidden("forum.moderation_forbidden", "Cannot moderate yourself")
	}
	action, reason := strings.TrimSpace(req.Action), strings.TrimSpace(req.Reason)
	switch action {
	case "warning", "ban":
		if reason == "" {
			return model.ForumUserModerationAction{}, apperr.BadRequest("validation.invalid_request", "reason is required")
		}
	case "silence":
		if reason == "" {
			return model.ForumUserModerationAction{}, apperr.BadRequest("validation.invalid_request", "reason is required")
		}
		if req.DurationHours < 1 || req.DurationHours > 2160 {
			return model.ForumUserModerationAction{}, apperr.BadRequest("validation.invalid_request", "duration_hours must be between 1 and 2160")
		}
	case "unsilence", "unban":
	default:
		return model.ForumUserModerationAction{}, apperr.BadRequest("validation.invalid_request", "invalid action")
	}

	var created model.ForumUserModerationAction
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var target model.User
		if err := findModerationTargetForUpdate(tx, userID, &target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("auth.user_not_found", "User not found")
			}
			return err
		}
		if actor.Role == authctx.RoleAdmin && authctx.RoleAtLeast(target.Role, authctx.RoleAdmin) {
			return apperr.Forbidden("forum.moderation_forbidden", "Cannot moderate an admin or owner")
		}
		if target.Role == authctx.RoleOwner {
			return apperr.Forbidden("forum.moderation_forbidden", "Cannot moderate an owner")
		}
		if action == "ban" && !target.IsActive {
			return apperr.Conflict("forum.user_already_banned", "User is already banned")
		}
		if action == "unban" && target.IsActive {
			return apperr.Conflict("forum.user_not_banned", "User is not banned")
		}
		if action == "unsilence" {
			active, err := IsUserSilenced(tx, userID, time.Now().UTC())
			if err != nil {
				return err
			}
			if !active {
				return apperr.Conflict("forum.user_not_silenced", "User is not silenced")
			}
		}
		if action == "silence" {
			active, err := IsUserSilenced(tx, userID, time.Now().UTC())
			if err != nil {
				return err
			}
			if active {
				return apperr.Conflict("forum.user_already_silenced", "User is already silenced")
			}
		}
		created = model.ForumUserModerationAction{UserID: userID, ActorID: actor.ID, Action: action, Reason: reason}
		if action == "silence" {
			expires := time.Now().UTC().Add(time.Duration(req.DurationHours) * time.Hour)
			created.ExpiresAt = &expires
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		if action == "ban" || action == "unban" {
			if err := tx.Model(&target).Update("is_active", action == "unban").Error; err != nil {
				return err
			}
		}
		if action == "warning" || action == "silence" || action == "ban" {
			meta := model.NotificationMeta{"action": action, "reason": reason, "expires_at": created.ExpiresAt}
			n := model.Notification{RecipientID: userID, ActorID: &actor.ID, Type: "forum_moderation", SourceType: "forum_moderation_action", SourceID: created.ID, Meta: meta}
			if err := tx.Create(&n).Error; err != nil {
				return err
			}
		}
		return audit.Record(tx, audit.Entry{ActorID: &actor.ID, Action: "forum.moderation." + action, EntityType: "user", EntityID: &userID, Reason: reason, Metadata: map[string]any{"expires_at": created.ExpiresAt}})
	})
	return created, err
}

func (s *Service) LockTopic(user authctx.CurrentUser, topicID uuid.UUID) (model.ForumTopic, error) {
	return s.setTopicClosed(user, topicID, true)
}

func (s *Service) UnlockTopic(user authctx.CurrentUser, topicID uuid.UUID) (model.ForumTopic, error) {
	return s.setTopicClosed(user, topicID, false)
}

func (s *Service) PinTopic(user authctx.CurrentUser, topicID uuid.UUID) (model.ForumTopic, error) {
	return s.setTopicPinned(user, topicID, true)
}

func (s *Service) UnpinTopic(user authctx.CurrentUser, topicID uuid.UUID) (model.ForumTopic, error) {
	return s.setTopicPinned(user, topicID, false)
}

func (s *Service) HideTopic(user authctx.CurrentUser, topicID uuid.UUID) (model.ForumTopic, error) {
	return s.setTopicHidden(user, topicID, true)
}

func (s *Service) RestoreTopic(user authctx.CurrentUser, topicID uuid.UUID) (model.ForumTopic, error) {
	return s.setTopicHidden(user, topicID, false)
}

func (s *Service) ListReports(user authctx.CurrentUser, query ListReportsQuery) ([]model.ForumReport, int64, error) {
	if err := s.requireAdminOrOwner(user); err != nil {
		return nil, 0, err
	}
	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	status := strings.TrimSpace(query.Status)
	if status == "" {
		status = "open"
	}

	q := s.db.Model(&model.ForumReport{})
	if status != "all" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var reports []model.ForumReport
	if err := q.Order("created_at ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&reports).Error; err != nil {
		return nil, 0, err
	}
	for index := range reports {
		if reports[index].TargetType == "topic" {
			topicID := reports[index].TargetID
			reports[index].TopicID = &topicID
		}
	}
	return reports, total, nil
}

func (s *Service) ListCategoryRequests(user authctx.CurrentUser) ([]model.CategoryRequest, error) {
	if err := s.canReviewCategoryRequest(user); err != nil {
		return nil, err
	}
	var requests []model.CategoryRequest
	if err := s.db.Where("status = ?", "pending").Preload("User").Order("created_at ASC").Find(&requests).Error; err != nil {
		return nil, err
	}
	return requests, nil
}

func (s *Service) ResolveReport(user authctx.CurrentUser, reportID uuid.UUID) (model.ForumReport, error) {
	return s.resolveReport(user, reportID, ResolveReportRequest{})
}

func (s *Service) CreateReport(user authctx.CurrentUser, req CreateReportRequest) (model.ForumReport, error) {
	if user.ID == uuid.Nil {
		return model.ForumReport{}, apperr.Unauthorized("Login required")
	}
	if req.TargetID == uuid.Nil {
		return model.ForumReport{}, apperr.BadRequest("validation.invalid_request", "target_id is required")
	}
	targetType := strings.TrimSpace(req.TargetType)
	if targetType != "topic" {
		return model.ForumReport{}, apperr.BadRequest("validation.invalid_request", "target_type must be topic")
	}
	reason := strings.TrimSpace(req.Reason)
	switch reason {
	case "spam", "off-topic", "harassment", "other":
	default:
		return model.ForumReport{}, apperr.BadRequest("validation.invalid_request", "reason is invalid")
	}
	if err := s.validateReportTarget(user, targetType, req.TargetID); err != nil {
		return model.ForumReport{}, err
	}

	var existing model.ForumReport
	if err := s.db.Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, targetType, req.TargetID).First(&existing).Error; err == nil {
		return model.ForumReport{}, apperr.Conflict("forum.report_exists", "already reported")
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ForumReport{}, err
	}

	report := model.ForumReport{
		UserID:     user.ID,
		TargetType: targetType,
		TargetID:   req.TargetID,
		Reason:     reason,
		Note:       strings.TrimSpace(req.Note),
	}
	if err := s.db.Create(&report).Error; err != nil {
		if isReportDuplicateError(err) {
			return model.ForumReport{}, apperr.Conflict("forum.report_exists", "already reported")
		}
		return model.ForumReport{}, err
	}

	const threshold = 10
	if targetType == "topic" {
		var count int64
		if err := s.db.Model(&model.ForumReport{}).Where("target_type = ? AND target_id = ? AND status = ?", "topic", req.TargetID, "open").Count(&count).Error; err != nil {
			return model.ForumReport{}, err
		}
		if count >= threshold {
			if err := s.db.Model(&model.ForumTopic{}).Where("id = ?", req.TargetID).Update("closed", true).Error; err != nil {
				return model.ForumReport{}, err
			}
		}
	}

	return report, nil
}

func (s *Service) validateReportTarget(user authctx.CurrentUser, targetType string, targetID uuid.UUID) error {
	var topic model.ForumTopic
	if err := s.db.Select("id", "category_id").First(&topic, "id = ?", targetID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("forum.topic_not_found", "Forum topic not found")
		}
		return err
	}
	categoryID := topic.CategoryID
	if authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return nil
	}
	var configured int64
	if err := s.db.Model(&model.ForumCategoryPermission{}).Where("category_id = ?", categoryID).Count(&configured).Error; err != nil {
		return err
	}
	if configured == 0 {
		return nil
	}
	var allowed int64
	if err := s.db.Table("forum_category_permissions fcp").
		Joins("JOIN forum_group_members fgm ON fgm.group_id = fcp.group_id AND fgm.deleted_at IS NULL").
		Where("fcp.category_id = ? AND fcp.can_view = ? AND fcp.deleted_at IS NULL AND fgm.user_id = ?", categoryID, true, user.ID).
		Count(&allowed).Error; err != nil {
		return err
	}
	if allowed == 0 {
		return apperr.Forbidden("forum.category_permission_denied", "Forum category permission denied")
	}
	return nil
}

func isReportDuplicateError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") && strings.Contains(message, "forum_reports")
}

func (s *Service) ResolveReportWithNote(user authctx.CurrentUser, reportID uuid.UUID, req ResolveReportRequest) (model.ForumReport, error) {
	return s.resolveReport(user, reportID, req)
}

func (s *Service) ApproveCategoryRequest(user authctx.CurrentUser, requestID uuid.UUID, req ReviewCategoryRequestInput) (model.CategoryRequest, *model.ForumCategory, error) {
	if err := s.canReviewCategoryRequest(user); err != nil {
		return model.CategoryRequest{}, nil, err
	}
	if requestID == uuid.Nil {
		return model.CategoryRequest{}, nil, apperr.BadRequest("validation.invalid_request", "request_id is required")
	}

	var reviewed model.CategoryRequest
	var createdCategory *model.ForumCategory
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var request model.CategoryRequest
		if err := tx.First(&request, "id = ?", requestID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("forum.category_request_not_found", "Forum category request not found")
			}
			return err
		}
		request.Status = "approved"
		request.ReviewedBy = &user.ID
		request.ReviewNote = strings.TrimSpace(req.ReviewNote)
		if err := tx.Save(&request).Error; err != nil {
			return err
		}
		color := strings.TrimSpace(req.Color)
		if color == "" {
			color = "#6366f1"
		}
		category := model.ForumCategory{
			Name:        request.Name,
			Description: request.Description,
			Color:       color,
		}
		if err := tx.Create(&category).Error; err != nil {
			return err
		}
		reviewed = request
		createdCategory = &category
		return nil
	}); err != nil {
		return model.CategoryRequest{}, nil, err
	}
	return reviewed, createdCategory, nil
}

func (s *Service) RejectCategoryRequest(user authctx.CurrentUser, requestID uuid.UUID, req ReviewCategoryRequestInput) (model.CategoryRequest, error) {
	if err := s.canReviewCategoryRequest(user); err != nil {
		return model.CategoryRequest{}, err
	}
	if requestID == uuid.Nil {
		return model.CategoryRequest{}, apperr.BadRequest("validation.invalid_request", "request_id is required")
	}
	var request model.CategoryRequest
	if err := s.db.First(&request, "id = ?", requestID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.CategoryRequest{}, apperr.NotFound("forum.category_request_not_found", "Forum category request not found")
		}
		return model.CategoryRequest{}, err
	}
	request.Status = "rejected"
	request.ReviewedBy = &user.ID
	request.ReviewNote = strings.TrimSpace(req.ReviewNote)
	if err := s.db.Save(&request).Error; err != nil {
		return model.CategoryRequest{}, err
	}
	return request, nil
}

func (s *Service) ListModeratorAssignments(user authctx.CurrentUser) ([]model.ForumModeratorAssignment, error) {
	if err := s.requireAdminOrOwner(user); err != nil {
		return nil, err
	}
	var assignments []model.ForumModeratorAssignment
	if err := s.db.Preload("User").Preload("Category").Order("created_at ASC").Find(&assignments).Error; err != nil {
		return nil, err
	}
	return assignments, nil
}

func (s *Service) CreateModeratorAssignment(user authctx.CurrentUser, input ModeratorAssignmentInput) (model.ForumModeratorAssignment, error) {
	if err := s.requireAdminOrOwner(user); err != nil {
		return model.ForumModeratorAssignment{}, err
	}
	assignment := model.ForumModeratorAssignment{
		UserID:                   input.UserID,
		CategoryID:               input.CategoryID,
		CanReviewCategoryRequest: input.CanReviewCategoryRequest,
		CanPinTopic:              input.CanPinTopic,
		CanLockTopic:             input.CanLockTopic,
	}
	if err := s.validateModeratorAssignment(&assignment); err != nil {
		return model.ForumModeratorAssignment{}, err
	}
	if err := s.db.Create(&assignment).Error; err != nil {
		return model.ForumModeratorAssignment{}, err
	}
	return s.loadModeratorAssignment(assignment.ID)
}

func (s *Service) UpdateModeratorAssignment(user authctx.CurrentUser, assignmentID uuid.UUID, input ModeratorAssignmentInput) (model.ForumModeratorAssignment, error) {
	if err := s.requireAdminOrOwner(user); err != nil {
		return model.ForumModeratorAssignment{}, err
	}
	if assignmentID == uuid.Nil {
		return model.ForumModeratorAssignment{}, apperr.BadRequest("validation.invalid_request", "assignment_id is required")
	}
	var assignment model.ForumModeratorAssignment
	if err := s.db.First(&assignment, "id = ?", assignmentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ForumModeratorAssignment{}, apperr.NotFound("forum.moderator_assignment_not_found", "Forum moderator assignment not found")
		}
		return model.ForumModeratorAssignment{}, err
	}
	assignment.UserID = input.UserID
	assignment.CategoryID = input.CategoryID
	assignment.CanReviewCategoryRequest = input.CanReviewCategoryRequest
	assignment.CanPinTopic = input.CanPinTopic
	assignment.CanLockTopic = input.CanLockTopic
	if err := s.validateModeratorAssignment(&assignment); err != nil {
		return model.ForumModeratorAssignment{}, err
	}
	if err := s.db.Save(&assignment).Error; err != nil {
		return model.ForumModeratorAssignment{}, err
	}
	return s.loadModeratorAssignment(assignment.ID)
}

func (s *Service) DeleteModeratorAssignment(user authctx.CurrentUser, assignmentID uuid.UUID) error {
	if err := s.requireAdminOrOwner(user); err != nil {
		return err
	}
	if assignmentID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "assignment_id is required")
	}
	result := s.db.Delete(&model.ForumModeratorAssignment{}, "id = ?", assignmentID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperr.NotFound("forum.moderator_assignment_not_found", "Forum moderator assignment not found")
	}
	return nil
}

func (s *Service) resolveReport(user authctx.CurrentUser, reportID uuid.UUID, req ResolveReportRequest) (model.ForumReport, error) {
	if err := s.requireAdminOrOwner(user); err != nil {
		return model.ForumReport{}, err
	}
	if reportID == uuid.Nil {
		return model.ForumReport{}, apperr.BadRequest("validation.invalid_request", "report_id is required")
	}
	var report model.ForumReport
	if err := s.db.First(&report, "id = ?", reportID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ForumReport{}, apperr.NotFound("forum.report_not_found", "Forum report not found")
		}
		return model.ForumReport{}, err
	}
	report.Status = "resolved"
	report.ReviewedBy = &user.ID
	report.ReviewNote = strings.TrimSpace(req.ReviewNote)
	if err := s.db.Save(&report).Error; err != nil {
		return model.ForumReport{}, err
	}
	return report, nil
}

func (s *Service) setTopicClosed(user authctx.CurrentUser, topicID uuid.UUID, closed bool) (model.ForumTopic, error) {
	if topicID == uuid.Nil {
		return model.ForumTopic{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	var topic model.ForumTopic
	if err := s.db.First(&topic, "id = ?", topicID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ForumTopic{}, apperr.NotFound("forum.topic_not_found", "Forum topic not found")
		}
		return model.ForumTopic{}, err
	}
	if err := s.canModerateCategory(user, &topic.CategoryID, func(assignment model.ForumModeratorAssignment) bool {
		return assignment.CanLockTopic
	}); err != nil {
		return model.ForumTopic{}, err
	}
	topic.Closed = closed
	if err := s.db.Save(&topic).Error; err != nil {
		return model.ForumTopic{}, err
	}
	return topic, nil
}

func (s *Service) setTopicPinned(user authctx.CurrentUser, topicID uuid.UUID, pinned bool) (model.ForumTopic, error) {
	if topicID == uuid.Nil {
		return model.ForumTopic{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	var topic model.ForumTopic
	if err := s.db.First(&topic, "id = ?", topicID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ForumTopic{}, apperr.NotFound("forum.topic_not_found", "Forum topic not found")
		}
		return model.ForumTopic{}, err
	}
	if err := s.canModerateCategory(user, &topic.CategoryID, func(assignment model.ForumModeratorAssignment) bool {
		return assignment.CanPinTopic
	}); err != nil {
		return model.ForumTopic{}, err
	}
	topic.Pinned = pinned
	if err := s.db.Save(&topic).Error; err != nil {
		return model.ForumTopic{}, err
	}
	return topic, nil
}

func (s *Service) setTopicHidden(user authctx.CurrentUser, topicID uuid.UUID, hidden bool) (model.ForumTopic, error) {
	if topicID == uuid.Nil {
		return model.ForumTopic{}, apperr.BadRequest("validation.invalid_request", "topic_id is required")
	}
	var topic model.ForumTopic
	query := s.db.Model(&model.ForumTopic{})
	if !hidden {
		query = query.Unscoped()
	}
	if err := query.First(&topic, "id = ?", topicID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ForumTopic{}, apperr.NotFound("forum.topic_not_found", "Forum topic not found")
		}
		return model.ForumTopic{}, err
	}
	if err := s.canModerateCategory(user, &topic.CategoryID, func(assignment model.ForumModeratorAssignment) bool {
		return assignment.CanLockTopic
	}); err != nil {
		return model.ForumTopic{}, err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if hidden {
			return tx.Model(&model.ForumTopic{}).Where("id = ?", topicID).Update("deleted_at", time.Now().UTC()).Error
		}
		return tx.Unscoped().Model(&model.ForumTopic{}).Where("id = ?", topicID).Update("deleted_at", nil).Error
	}); err != nil {
		return model.ForumTopic{}, err
	}
	if err := s.db.Unscoped().First(&topic, "id = ?", topicID).Error; err != nil {
		return model.ForumTopic{}, err
	}
	return topic, nil
}

func (s *Service) requireAdminOrOwner(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("auth.forbidden", "Admin permission required")
	}
	return nil
}

func (s *Service) canReviewCategoryRequest(user authctx.CurrentUser) error {
	return s.canModerateCategory(user, nil, func(assignment model.ForumModeratorAssignment) bool {
		return assignment.CanReviewCategoryRequest
	})
}

func (s *Service) canModerateCategory(user authctx.CurrentUser, categoryID *uuid.UUID, allow func(model.ForumModeratorAssignment) bool) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return nil
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return apperr.Forbidden("auth.forbidden", "Moderator permission required")
	}

	query := s.db.Where("user_id = ?", user.ID)
	if categoryID != nil {
		query = query.Where("category_id IS NULL OR category_id = ?", *categoryID)
	} else {
		query = query.Where("category_id IS NULL")
	}

	var assignments []model.ForumModeratorAssignment
	if err := query.Find(&assignments).Error; err != nil {
		return err
	}
	for _, assignment := range assignments {
		if allow(assignment) {
			return nil
		}
	}
	return apperr.Forbidden("auth.forbidden", "Moderator permission required")
}

func (s *Service) validateModeratorAssignment(assignment *model.ForumModeratorAssignment) error {
	if assignment.UserID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "user_id is required")
	}
	var user model.User
	if err := s.db.First(&user, "uuid = ?", assignment.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("auth.user_not_found", "User not found")
		}
		return err
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return apperr.BadRequest("validation.invalid_request", "user must be moderator or above")
	}
	if assignment.CategoryID != nil {
		var category model.ForumCategory
		if err := s.db.First(&category, "id = ?", *assignment.CategoryID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("forum.category_not_found", "Forum category not found")
			}
			return err
		}
	}
	return nil
}

func (s *Service) loadModeratorAssignment(id uuid.UUID) (model.ForumModeratorAssignment, error) {
	var assignment model.ForumModeratorAssignment
	if err := s.db.Preload("User").Preload("Category").First(&assignment, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ForumModeratorAssignment{}, apperr.NotFound("forum.moderator_assignment_not_found", "Forum moderator assignment not found")
		}
		return model.ForumModeratorAssignment{}, err
	}
	return assignment, nil
}
