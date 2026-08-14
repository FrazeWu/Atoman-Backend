package feed

import (
	"net/http"
	"strings"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const defaultSubscriptionGroupName = "默认分组"

func nextSubscriptionGroupPosition(db *gorm.DB, userID uuid.UUID) int {
	var position int
	db.Model(&model.SubscriptionGroup{}).Where("user_id = ?", userID).Select("COALESCE(MAX(position), -1)").Scan(&position)
	return position + 1
}

func getOrCreateDefaultSubscriptionGroup(db *gorm.DB, userID uuid.UUID) (*model.SubscriptionGroup, error) {
	var canonical model.SubscriptionGroup

	err := db.Transaction(func(tx *gorm.DB) error {
		var groups []model.SubscriptionGroup
		if err := tx.Where("user_id = ? AND name = ?", userID, defaultSubscriptionGroupName).
			Order("created_at ASC").Find(&groups).Error; err != nil {
			return err
		}

		switch len(groups) {
		case 0:
			canonical = model.SubscriptionGroup{
				UserID: userID,
				Name:   defaultSubscriptionGroupName,
			}
			result := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "user_id"}, {Name: "name"}},
				TargetWhere: clause.Where{Exprs: []clause.Expression{
					clause.Eq{Column: clause.Column{Name: "deleted_at"}, Value: nil},
				}},
				DoNothing: true,
			}).Create(&canonical)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				if err := tx.Where("user_id = ? AND name = ?", userID, defaultSubscriptionGroupName).
					Order("created_at ASC").First(&canonical).Error; err != nil {
					return err
				}
				return nil
			}
		case 1:
			canonical = groups[0]
		default:
			canonical = groups[0]
			duplicateIDs := make([]uuid.UUID, 0, len(groups)-1)
			for _, g := range groups[1:] {
				duplicateIDs = append(duplicateIDs, g.ID)
			}

			if err := tx.Model(&model.Subscription{}).
				Where("user_id = ? AND subscription_group_id IN ?", userID, duplicateIDs).
				Update("subscription_group_id", canonical.ID).Error; err != nil {
				return err
			}

			if err := tx.Where("user_id = ? AND id IN ?", userID, duplicateIDs).
				Delete(&model.SubscriptionGroup{}).Error; err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &canonical, nil
}

// GetSubscriptionGroups godoc
// @Summary 获取订阅分组列表
// @Description 返回当前用户的订阅分组。
// @Tags feed
// @Produce json
// @Success 200 {object} SubscriptionGroupListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/groups [get]
func GetSubscriptionGroups(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if _, err := getOrCreateDefaultSubscriptionGroup(db, userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare default group"})
			return
		}

		var groups []model.SubscriptionGroup
		if err := db.Where("user_id = ?", userID).Order("position ASC, created_at ASC").Find(&groups).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch groups"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": groups, "message": "ok"})
	}
}

type GroupInput struct {
	Name string `json:"name" binding:"required"`
}

// CreateSubscriptionGroup godoc
// @Summary 创建订阅分组
// @Description 为当前用户创建一个订阅分组。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body GroupInput true "分组输入"
// @Success 201 {object} SubscriptionGroupResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/groups [post]
func CreateSubscriptionGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input GroupInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		name := strings.TrimSpace(input.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Group name is required"})
			return
		}

		if name == defaultSubscriptionGroupName {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Default group already exists"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var existing model.SubscriptionGroup
		if err := db.Where("user_id = ? AND name = ?", userID, name).First(&existing).Error; err == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Group name already exists"})
			return
		} else if err != gorm.ErrRecordNotFound {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to validate group name"})
			return
		}

		group := model.SubscriptionGroup{
			UserID:   userID,
			Name:     name,
			Position: nextSubscriptionGroupPosition(db, userID),
		}

		if err := db.Create(&group).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create group"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": group, "message": "ok"})
	}
}

// UpdateSubscriptionGroup godoc
// @Summary 更新订阅分组
// @Description 重命名当前用户的订阅分组。
// @Tags feed
// @Accept json
// @Produce json
// @Param id path string true "分组 UUID"
// @Param input body GroupInput true "分组输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/groups/{id} [put]
func UpdateSubscriptionGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var input GroupInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		name := strings.TrimSpace(input.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Group name is required"})
			return
		}

		var target model.SubscriptionGroup
		if err := db.Where("id = ? AND user_id = ?", id, userID).First(&target).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Group not found"})
			return
		}

		if target.Name == defaultSubscriptionGroupName {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Default group cannot be renamed"})
			return
		}

		if name == defaultSubscriptionGroupName {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Default group name is reserved"})
			return
		}

		var existing model.SubscriptionGroup
		if err := db.Where("user_id = ? AND name = ? AND id <> ?", userID, name, id).First(&existing).Error; err == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Group name already exists"})
			return
		} else if err != gorm.ErrRecordNotFound {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to validate group name"})
			return
		}

		if err := db.Model(&model.SubscriptionGroup{}).Where("id = ? AND user_id = ?", id, userID).Update("name", name).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update group"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// DeleteSubscriptionGroup godoc
// @Summary 删除订阅分组
// @Description 删除分组并将其中订阅迁回默认分组。
// @Tags feed
// @Produce json
// @Param id path string true "分组 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/groups/{id} [delete]
func DeleteSubscriptionGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var targetGroup model.SubscriptionGroup
		if err := db.Where("id = ? AND user_id = ?", id, userID).First(&targetGroup).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Group not found"})
			return
		}

		if targetGroup.Name == defaultSubscriptionGroupName {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Default group cannot be deleted"})
			return
		}

		defaultGroup, err := getOrCreateDefaultSubscriptionGroup(db, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare default group"})
			return
		}

		db.Model(&model.Subscription{}).
			Where("subscription_group_id = ? AND user_id = ?", id, userID).
			Update("subscription_group_id", defaultGroup.ID)

		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.SubscriptionGroup{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete group"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

type SetGroupInput struct {
	GroupID *uuid.UUID `json:"group_id"`
}

// SetSubscriptionGroup godoc
// @Summary 设置订阅分组
// @Description 为某个订阅设置所属分组。
// @Tags feed
// @Accept json
// @Produce json
// @Param id path string true "订阅 UUID"
// @Param input body SetGroupInput true "分组设置输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/{id}/group [put]
func SetSubscriptionGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		subID := c.Param("id")
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var input SetGroupInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var targetGroupID *uuid.UUID
		if input.GroupID == nil {
			defaultGroup, err := getOrCreateDefaultSubscriptionGroup(db, userID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare default group"})
				return
			}
			targetGroupID = &defaultGroup.ID
		} else {
			var group model.SubscriptionGroup
			if err := db.Where("id = ? AND user_id = ?", *input.GroupID, userID).First(&group).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription group not found"})
				return
			}
			targetGroupID = input.GroupID
		}

		if err := db.Model(&model.Subscription{}).
			Where("id = ? AND user_id = ?", subID, userID).
			Update("subscription_group_id", targetGroupID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update subscription group"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}
