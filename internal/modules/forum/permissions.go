package forum

import (
	"errors"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) CanCreateTopic(user authctx.CurrentUser, categoryID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.requireCategoryPermission(user, categoryID, "can_create_topic")
}

func (s *Service) CanComment(user authctx.CurrentUser, categoryID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.requireCategoryPermission(user, categoryID, "can_comment")
}

func (s *Service) CanViewCategory(user authctx.CurrentUser, categoryID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.requireCategoryPermission(user, categoryID, "can_view")
}

func (s *Service) requireCategoryPermission(user authctx.CurrentUser, categoryID uuid.UUID, field string) error {
	if categoryID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "category_id is required")
	}
	var category model.ForumCategory
	if err := s.db.First(&category, "id = ?", categoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("forum.category_not_found", "Forum category not found")
		}
		return err
	}
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
	err := s.db.Table("forum_category_permissions fcp").
		Joins("JOIN forum_group_members fgm ON fgm.group_id = fcp.group_id AND fgm.deleted_at IS NULL").
		Where("fcp.category_id = ? AND fcp."+field+" = ? AND fcp.deleted_at IS NULL AND fgm.user_id = ?", categoryID, true, user.ID).
		Count(&allowed).Error
	if err != nil {
		return err
	}
	if allowed == 0 {
		return apperr.Forbidden("forum.category_permission_denied", "Forum category permission denied")
	}
	return nil
}

func (s *Service) ListGroups(user authctx.CurrentUser) ([]model.ForumGroup, error) {
	if err := requireForumAdmin(user); err != nil {
		return nil, err
	}
	var groups []model.ForumGroup
	err := s.db.Preload("Members.User").Order("name ASC").Find(&groups).Error
	return groups, err
}

func (s *Service) CreateGroup(user authctx.CurrentUser, req UpsertForumGroupRequest) (model.ForumGroup, error) {
	if err := requireForumAdmin(user); err != nil {
		return model.ForumGroup{}, err
	}
	group, err := normalizedGroup(req)
	if err != nil {
		return model.ForumGroup{}, err
	}
	if err := s.db.Create(&group).Error; err != nil {
		if isForumGroupDuplicateError(err) {
			return model.ForumGroup{}, apperr.Conflict("forum.group_name_conflict", "Forum group name already exists")
		}
		return model.ForumGroup{}, err
	}
	return group, nil
}

func (s *Service) UpdateGroup(user authctx.CurrentUser, groupID uuid.UUID, req UpsertForumGroupRequest) (model.ForumGroup, error) {
	if err := requireForumAdmin(user); err != nil {
		return model.ForumGroup{}, err
	}
	group, err := normalizedGroup(req)
	if err != nil {
		return model.ForumGroup{}, err
	}
	var existing model.ForumGroup
	if err := s.db.First(&existing, "id = ?", groupID).Error; err != nil {
		return model.ForumGroup{}, mapGroupNotFound(err)
	}
	if err := s.db.Model(&model.ForumGroup{}).Where("id = ?", groupID).Updates(map[string]any{"name": group.Name, "description": group.Description}).Error; err != nil {
		if isForumGroupDuplicateError(err) {
			return model.ForumGroup{}, apperr.Conflict("forum.group_name_conflict", "Forum group name already exists")
		}
		return model.ForumGroup{}, err
	}
	if err := s.db.Preload("Members.User").First(&group, "id = ?", groupID).Error; err != nil {
		return model.ForumGroup{}, err
	}
	return group, nil
}

func (s *Service) DeleteGroup(user authctx.CurrentUser, groupID uuid.UUID) error {
	if err := requireForumAdmin(user); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.ForumGroup{}).Where("id = ?", groupID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return mapGroupNotFound(gorm.ErrRecordNotFound)
		}
		if err := tx.Unscoped().Where("group_id = ?", groupID).Delete(&model.ForumCategoryPermission{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("group_id = ?", groupID).Delete(&model.ForumGroupMember{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&model.ForumGroup{}, "id = ?", groupID).Error
	})
}

func (s *Service) AddGroupMember(user authctx.CurrentUser, groupID, userID uuid.UUID) error {
	if err := requireForumAdmin(user); err != nil {
		return err
	}
	if groupID == uuid.Nil || userID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "group_id and user_id are required")
	}
	if err := s.ensureGroupAndUser(groupID, userID); err != nil {
		return err
	}
	member := model.ForumGroupMember{GroupID: groupID, UserID: userID}
	return s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&member).Error
}

func (s *Service) RemoveGroupMember(user authctx.CurrentUser, groupID, userID uuid.UUID) error {
	if err := requireForumAdmin(user); err != nil {
		return err
	}
	return s.db.Unscoped().Where("group_id = ? AND user_id = ?", groupID, userID).Delete(&model.ForumGroupMember{}).Error
}

func (s *Service) ListCategoryPermissions(user authctx.CurrentUser, categoryID uuid.UUID) ([]model.ForumCategoryPermission, error) {
	if err := requireForumAdmin(user); err != nil {
		return nil, err
	}
	db := s.db.Preload("Category").Preload("Group").Order("created_at ASC")
	if categoryID != uuid.Nil {
		db = db.Where("category_id = ?", categoryID)
	}
	var permissions []model.ForumCategoryPermission
	err := db.Find(&permissions).Error
	return permissions, err
}

func (s *Service) PutCategoryPermission(user authctx.CurrentUser, req PutCategoryPermissionRequest) (model.ForumCategoryPermission, error) {
	if err := requireForumAdmin(user); err != nil {
		return model.ForumCategoryPermission{}, err
	}
	if req.CategoryID == uuid.Nil || req.GroupID == uuid.Nil {
		return model.ForumCategoryPermission{}, apperr.BadRequest("validation.invalid_request", "category_id and group_id are required")
	}
	if !req.CanView && (req.CanCreateTopic || req.CanComment) {
		return model.ForumCategoryPermission{}, apperr.BadRequest("validation.invalid_request", "view permission is required for posting or commenting")
	}
	if err := s.ensureCategoryAndGroup(req.CategoryID, req.GroupID); err != nil {
		return model.ForumCategoryPermission{}, err
	}
	permission := model.ForumCategoryPermission{
		CategoryID: req.CategoryID, GroupID: req.GroupID, CanView: req.CanView,
		CanCreateTopic: req.CanCreateTopic, CanComment: req.CanComment,
	}
	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "category_id"}, {Name: "group_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"can_view", "can_create_topic", "can_comment", "updated_at", "deleted_at"}),
	}).Create(&permission).Error
	if err != nil {
		return model.ForumCategoryPermission{}, err
	}
	var stored model.ForumCategoryPermission
	if err := s.db.Preload("Category").Preload("Group").Where("category_id = ? AND group_id = ?", req.CategoryID, req.GroupID).First(&stored).Error; err != nil {
		return model.ForumCategoryPermission{}, err
	}
	return stored, nil
}

func (s *Service) DeleteCategoryPermission(user authctx.CurrentUser, permissionID uuid.UUID) error {
	if err := requireForumAdmin(user); err != nil {
		return err
	}
	result := s.db.Delete(&model.ForumCategoryPermission{}, "id = ?", permissionID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperr.NotFound("forum.category_permission_not_found", "Forum category permission not found")
	}
	return nil
}

func normalizedGroup(req UpsertForumGroupRequest) (model.ForumGroup, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return model.ForumGroup{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	return model.ForumGroup{Name: name, Description: strings.TrimSpace(req.Description)}, nil
}

func isForumGroupDuplicateError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return true
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == "23505" {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") && strings.Contains(message, "forum_groups")
}

func requireForumAdmin(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("auth.forbidden", "Admin permission required")
	}
	return nil
}

func mapGroupNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("forum.group_not_found", "Forum group not found")
	}
	return err
}

func (s *Service) ensureGroupAndUser(groupID, userID uuid.UUID) error {
	var group model.ForumGroup
	if err := s.db.First(&group, "id = ?", groupID).Error; err != nil {
		return mapGroupNotFound(err)
	}
	var user model.User
	if err := s.db.First(&user, "uuid = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("auth.user_not_found", "User not found")
		}
		return err
	}
	return nil
}

func (s *Service) ensureCategoryAndGroup(categoryID, groupID uuid.UUID) error {
	var category model.ForumCategory
	if err := s.db.First(&category, "id = ?", categoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("forum.category_not_found", "Forum category not found")
		}
		return err
	}
	var group model.ForumGroup
	if err := s.db.First(&group, "id = ?", groupID).Error; err != nil {
		return mapGroupNotFound(err)
	}
	return nil
}
