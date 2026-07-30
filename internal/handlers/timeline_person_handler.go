package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"atoman/internal/model"
)

// ====== Person Handlers ======

// GetTimelinePersons godoc
// @Summary 获取时间线人物列表
// @Description 分页返回公开人物，支持按名称搜索。
// @Tags timeline
// @Produce json
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Param search query string false "名称搜索"
// @Success 200 {object} TimelinePersonListResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/timeline/persons [get]
func GetTimelinePersons(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if page < 1 {
			page = 1
		}
		if limit < 1 || limit > 100 {
			limit = 20
		}
		offset := (page - 1) * limit

		search := c.Query("search")
		query := db.Model(&model.TimelinePerson{}).Preload("User").Where("is_public = ?", true)

		if search != "" {
			query = query.Where("name ILIKE ?", "%"+search+"%")
		}

		var total int64
		query.Count(&total)

		var persons []model.TimelinePerson
		if err := query.Order("name ASC").Limit(limit).Offset(offset).Find(&persons).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch persons"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data":  persons,
			"total": total,
			"page":  page,
			"limit": limit,
		})
	}
}

// GetTimelinePerson godoc
// @Summary 获取时间线人物详情
// @Description 返回人物详情及其地点轨迹。
// @Tags timeline
// @Produce json
// @Param id path string true "人物 UUID"
// @Success 200 {object} TimelinePersonResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/timeline/persons/{id} [get]
func GetTimelinePerson(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var person model.TimelinePerson

		if err := db.Preload("User").First(&person, "id = ? AND is_public = ?", id, true).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
			return
		}

		var locations []model.PersonLocation
		db.Where("person_id = ?", person.ID).Order("date ASC").Find(&locations)
		person.Locations = locations

		c.JSON(http.StatusOK, gin.H{"data": person})
	}
}

// GetPersonLocations godoc
// @Summary 获取人物地点轨迹
// @Description 返回某个人物的地点轨迹列表。
// @Tags timeline
// @Produce json
// @Param id path string true "人物 UUID"
// @Success 200 {object} PersonLocationListResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/timeline/persons/{id}/locations [get]
func GetPersonLocations(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var person model.TimelinePerson
		if err := db.First(&person, "id = ? AND is_public = ?", id, true).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
			return
		}

		var locations []model.PersonLocation
		if err := db.Where("person_id = ?", id).Order("date ASC").Find(&locations).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch locations"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": locations})
	}
}

type CreatePersonInput struct {
	Name      string   `json:"name" binding:"required"`
	Bio       string   `json:"bio"`
	BirthDate string   `json:"birth_date"`
	DeathDate string   `json:"death_date"`
	Tags      []string `json:"tags"`
	IsPublic  *bool    `json:"is_public"`
}

// CreateTimelinePerson godoc
// @Summary 创建时间线人物
// @Description 创建一个时间线人物条目。
// @Tags timeline
// @Accept json
// @Produce json
// @Param input body CreatePersonInput true "人物输入"
// @Success 201 {object} TimelinePersonResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/persons [post]
func CreateTimelinePerson(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input CreatePersonInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		isPublic := true
		if input.IsPublic != nil {
			isPublic = *input.IsPublic
		}

		userID, _ := c.Get("user_id")
		person := model.TimelinePerson{
			UserID:   userID.(uuid.UUID),
			Name:     input.Name,
			Bio:      input.Bio,
			Tags:     pq.StringArray(input.Tags),
			IsPublic: isPublic,
		}

		if input.BirthDate != "" {
			if d, err := parseDateTime(input.BirthDate); err == nil {
				person.BirthDate = &d
			}
		}
		if input.DeathDate != "" {
			if d, err := parseDateTime(input.DeathDate); err == nil {
				person.DeathDate = &d
			}
		}

		if err := db.Create(&person).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create person"})
			return
		}

		db.Preload("User").First(&person, person.ID)
		c.JSON(http.StatusCreated, gin.H{"data": person})
	}
}

// UpdateTimelinePerson godoc
// @Summary 更新时间线人物
// @Description 人物作者或管理员可以更新时间线人物信息。
// @Tags timeline
// @Accept json
// @Produce json
// @Param id path string true "人物 UUID"
// @Param input body CreatePersonInput true "人物输入"
// @Success 200 {object} TimelinePersonResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/persons/{id} [put]
func UpdateTimelinePerson(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var person model.TimelinePerson

		if err := db.First(&person, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
			return
		}

		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		if person.UserID != userID.(uuid.UUID) && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		var input CreatePersonInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]interface{}{
			"name": input.Name,
			"bio":  input.Bio,
			"tags": input.Tags,
		}

		if input.IsPublic != nil {
			updates["is_public"] = *input.IsPublic
		}

		if input.BirthDate != "" {
			if d, err := parseDateTime(input.BirthDate); err == nil {
				updates["birth_date"] = d
			}
		} else {
			updates["birth_date"] = nil
		}

		if input.DeathDate != "" {
			if d, err := parseDateTime(input.DeathDate); err == nil {
				updates["death_date"] = d
			}
		} else {
			updates["death_date"] = nil
		}

		if err := db.Model(&person).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update person"})
			return
		}

		db.Preload("User").First(&person, person.ID)
		c.JSON(http.StatusOK, gin.H{"data": person})
	}
}

// DeleteTimelinePerson godoc
// @Summary 删除时间线人物
// @Description 人物作者或管理员可以删除人物及其地点轨迹。
// @Tags timeline
// @Produce json
// @Param id path string true "人物 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/persons/{id} [delete]
func DeleteTimelinePerson(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var person model.TimelinePerson

		if err := db.First(&person, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
			return
		}

		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		if person.UserID != userID.(uuid.UUID) && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		db.Where("person_id = ?", id).Delete(&model.PersonLocation{})
		db.Delete(&person)
		c.JSON(http.StatusOK, gin.H{"message": "Person deleted"})
	}
}
