package forum_moderation

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ListReportsQuery struct {
	Page     int    `json:"page" form:"page"`
	PageSize int    `json:"page_size" form:"page_size"`
	Status   string `json:"status" form:"status"`
}

type ResolveReportRequest struct {
	ReviewNote string `json:"review_note"`
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

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
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

func (s *Service) HideReply(user authctx.CurrentUser, replyID uuid.UUID) (model.ForumReply, error) {
	return s.setReplyHidden(user, replyID, true)
}

func (s *Service) RestoreReply(user authctx.CurrentUser, replyID uuid.UUID) (model.ForumReply, error) {
	return s.setReplyHidden(user, replyID, false)
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
	return reports, total, nil
}

func (s *Service) ResolveReport(user authctx.CurrentUser, reportID uuid.UUID) (model.ForumReport, error) {
	return s.resolveReport(user, reportID, ResolveReportRequest{})
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
			deletedAt := time.Now().UTC()
			if err := tx.Model(&model.ForumReply{}).Where("topic_id = ?", topicID).Update("deleted_at", deletedAt).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.ForumTopic{}).Where("id = ?", topicID).Update("deleted_at", deletedAt).Error; err != nil {
				return err
			}
		} else {
			deletedAt := topic.DeletedAt.Time
			if err := tx.Unscoped().Model(&model.ForumTopic{}).Where("id = ?", topicID).Update("deleted_at", nil).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Model(&model.ForumReply{}).Where("topic_id = ? AND deleted_at = ?", topicID, deletedAt).Update("deleted_at", nil).Error; err != nil {
				return err
			}
		}
		return s.recalculateTopicReplyState(tx, topicID)
	}); err != nil {
		return model.ForumTopic{}, err
	}
	if err := s.db.Unscoped().First(&topic, "id = ?", topicID).Error; err != nil {
		return model.ForumTopic{}, err
	}
	return topic, nil
}

func (s *Service) setReplyHidden(user authctx.CurrentUser, replyID uuid.UUID, hidden bool) (model.ForumReply, error) {
	if replyID == uuid.Nil {
		return model.ForumReply{}, apperr.BadRequest("validation.invalid_request", "reply_id is required")
	}
	var reply model.ForumReply
	query := s.db.Model(&model.ForumReply{}).Preload("Topic")
	if !hidden {
		query = query.Unscoped()
	}
	if err := query.First(&reply, "id = ?", replyID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ForumReply{}, apperr.NotFound("forum.reply_not_found", "Forum reply not found")
		}
		return model.ForumReply{}, err
	}
	categoryID := reply.Topic.CategoryID
	if err := s.canModerateCategory(user, &categoryID, func(assignment model.ForumModeratorAssignment) bool {
		return assignment.CanLockTopic
	}); err != nil {
		return model.ForumReply{}, err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if hidden {
			if err := tx.Delete(&reply).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Unscoped().Model(&model.ForumReply{}).Where("id = ?", replyID).Update("deleted_at", nil).Error; err != nil {
				return err
			}
		}
		return s.recalculateTopicReplyState(tx, reply.TopicID)
	}); err != nil {
		return model.ForumReply{}, err
	}
	if err := s.db.Unscoped().Preload("Topic").First(&reply, "id = ?", replyID).Error; err != nil {
		return model.ForumReply{}, err
	}
	return reply, nil
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
	} else {
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
	}

	return tx.Unscoped().Model(&model.ForumTopic{}).Where("id = ?", topicID).Updates(updates).Error
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
