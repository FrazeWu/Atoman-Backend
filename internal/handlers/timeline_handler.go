package handlers

import (
	// GetTimelineEvents godoc
	// @Summary 获取时间线事件列表
	// @Description 分页返回公开时间线事件，支持分类和年份范围筛选。
	// @Tags timeline
	// @Produce json
	// @Param page query int false "页码"
	// @Param limit query int false "每页数量"
	// @Param category query string false "分类"
	// @Param year_start query int false "起始年份"
	// @Param year_end query int false "结束年份"
	// @Success 200 {object} TimelineEventListResponse
	// @Failure 500 {object} ErrorResponse
	// @Router /api/v1/timeline/events [get]
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	proposalservice "atoman/internal/service"
	timelinecore "atoman/internal/timeline"
)

// parseDateTime 尝试多种格式解析时间，支持精确到小时分钟，支持负年份（BCE）
// 负年份格式示例："-0500-01-01"（公元前500年）
func parseDateTime(s string) (time.Time, error) {
	return timelinecore.ParseDateTime(s)
}

func SetupTimelineRoutes(router *gin.Engine, db *gorm.DB) {
	tl := router.Group("/api/v1/timeline")
	proposalService := proposalservice.NewTimelineRevisionProposalService(db)
	{
		// Public routes
		tl.GET("/events", GetTimelineEvents(db))
		tl.GET("/events/:id", GetTimelineEvent(db))
		tl.GET("/persons", GetTimelinePersons(db))
		tl.GET("/persons/:id", GetTimelinePerson(db))
		tl.GET("/persons/:id/locations", GetPersonLocations(db))
		tl.GET("/events/:id/revision-proposals", middleware.OptionalAuthMiddleware(), ListTimelineEventProposals(proposalService))
		tl.GET("/persons/:id/revision-proposals", middleware.OptionalAuthMiddleware(), ListTimelinePersonProposals(proposalService))

		// Protected routes
		protected := tl.Group("")
		protected.Use(middleware.AuthMiddleware())
		{
			protected.POST("/events", CreateTimelineEvent(db))
			protected.POST("/events/:id/revision-proposals", CreateTimelineEventProposal(proposalService))
			protected.PUT("/events/:id", UpdateTimelineEvent(db))
			protected.DELETE("/events/:id", DeleteTimelineEvent(db))
			protected.GET("/events/:id/history", GetTimelineEventHistory(db))
			protected.POST("/events/:id/revert/:revision_id", RevertTimelineEvent(db))

			protected.POST("/persons", CreateTimelinePerson(db))
			protected.POST("/persons/:id/revision-proposals", CreateTimelinePersonProposal(proposalService))
			protected.PUT("/persons/:id", UpdateTimelinePerson(db))
			protected.DELETE("/persons/:id", DeleteTimelinePerson(db))
			protected.PUT("/revision-proposals/:comment_id/decision", DecideTimelineRevisionProposal(proposalService))

			protected.POST("/persons/:id/locations", AddPersonLocation(db))
			protected.PUT("/locations/:id", UpdatePersonLocation(db))
			protected.DELETE("/locations/:id", DeletePersonLocation(db))
		}
	}
}

// ====== Event Handlers ======

func GetTimelineEvents(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		if page < 1 {
			page = 1
		}
		if limit < 1 || limit > 200 {
			limit = 50
		}
		offset := (page - 1) * limit

		category := c.Query("category")
		yearStart := c.Query("year_start")
		yearEnd := c.Query("year_end")

		query := db.Model(&model.TimelineEvent{}).Preload("User").Where("is_public = ?", true)

		if category != "" {
			query = query.Where("category = ?", category)
		}
		if yearStart != "" {
			if y, err := strconv.Atoi(yearStart); err == nil {
				query = query.Where("event_date >= ?", time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC))
			}
		}
		if yearEnd != "" {
			if y, err := strconv.Atoi(yearEnd); err == nil {
				query = query.Where("event_date <= ?", time.Date(y, 12, 31, 23, 59, 59, 0, time.UTC))
			}
		}

		var total int64
		query.Count(&total)

		var events []model.TimelineEvent
		if err := query.Order("event_date ASC").Limit(limit).Offset(offset).Find(&events).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch events"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data":  events,
			"total": total,
			"page":  page,
			"limit": limit,
		})
	}
}

// GetTimelineEvent godoc
// @Summary 获取时间线事件详情
// @Description 返回单个时间线事件详情。
// @Tags timeline
// @Produce json
// @Param id path string true "事件 UUID"
// @Success 200 {object} TimelineEventResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/timeline/events/{id} [get]
func GetTimelineEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var event model.TimelineEvent
		if err := db.Preload("User").First(&event, "id = ? AND is_public = ?", id, true).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Event not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": event})
	}
}

type CreateEventInput struct {
	Title       string   `json:"title" binding:"required"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	EventDate   string   `json:"event_date" binding:"required"`
	EndDate     string   `json:"end_date"`
	Location    string   `json:"location" binding:"required"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	Source      string   `json:"source" binding:"required"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
	IsPublic    *bool    `json:"is_public"`
}

// CreateTimelineEvent godoc
// @Summary 创建时间线事件
// @Description 创建时间线事件并保存首个修订快照。
// @Tags timeline
// @Accept json
// @Produce json
// @Param input body CreateEventInput true "事件输入"
// @Success 201 {object} TimelineEventResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/events [post]
func CreateTimelineEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input CreateEventInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		eventDate, err := parseDateTime(input.EventDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid event_date format"})
			return
		}

		isPublic := true
		if input.IsPublic != nil {
			isPublic = *input.IsPublic
		}

		userID, _ := c.Get("user_id")
		event := model.TimelineEvent{
			UserID:      userID.(uuid.UUID),
			Title:       input.Title,
			Description: input.Description,
			Content:     input.Content,
			EventDate:   eventDate,
			Location:    input.Location,
			Latitude:    input.Latitude,
			Longitude:   input.Longitude,
			Source:      input.Source,
			Category:    input.Category,
			Tags:        pq.StringArray(input.Tags),
			IsPublic:    isPublic,
		}

		if input.EndDate != "" {
			if endDate, err := parseDateTime(input.EndDate); err == nil {
				event.EndDate = &endDate
			}
		}

		if err := db.Create(&event).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create event"})
			return
		}

		db.Preload("User").First(&event, event.ID)
		// Save initial revision
		saveEventRevision(db, event, userID.(uuid.UUID))
		c.JSON(http.StatusCreated, gin.H{"data": event})
	}
}

// UpdateTimelineEvent godoc
// @Summary 更新时间线事件
// @Description 事件作者或管理员可以更新时间线事件。
// @Tags timeline
// @Accept json
// @Produce json
// @Param id path string true "事件 UUID"
// @Param input body CreateEventInput true "事件输入"
// @Success 200 {object} TimelineEventResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/events/{id} [put]
func UpdateTimelineEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var event model.TimelineEvent

		if err := db.First(&event, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Event not found"})
			return
		}

		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		if event.UserID != userID.(uuid.UUID) && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		var input CreateEventInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		eventDate, err := parseDateTime(input.EventDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid event_date format"})
			return
		}

		updates := map[string]interface{}{
			"title":       input.Title,
			"description": input.Description,
			"content":     input.Content,
			"event_date":  eventDate,
			"location":    input.Location,
			"latitude":    input.Latitude,
			"longitude":   input.Longitude,
			"source":      input.Source,
			"category":    input.Category,
			"tags":        input.Tags,
		}

		if input.IsPublic != nil {
			updates["is_public"] = *input.IsPublic
		}

		if input.EndDate != "" {
			if endDate, err := parseDateTime(input.EndDate); err == nil {
				updates["end_date"] = endDate
			}
		} else {
			updates["end_date"] = nil
		}

		if err := db.Model(&event).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update event"})
			return
		}

		db.Preload("User").First(&event, event.ID)
		// Save revision snapshot after update
		saveEventRevision(db, event, userID.(uuid.UUID))
		c.JSON(http.StatusOK, gin.H{"data": event})
	}
}

// DeleteTimelineEvent godoc
// @Summary 删除时间线事件
// @Description 事件作者或管理员可以删除时间线事件。
// @Tags timeline
// @Produce json
// @Param id path string true "事件 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/events/{id} [delete]
func DeleteTimelineEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var event model.TimelineEvent

		if err := db.First(&event, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Event not found"})
			return
		}

		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		if event.UserID != userID.(uuid.UUID) && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		db.Delete(&event)
		c.JSON(http.StatusOK, gin.H{"message": "Event deleted"})
	}
}

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

// ====== Location Handlers ======

type CreateLocationInput struct {
	Date      string  `json:"date" binding:"required"`
	EndDate   string  `json:"end_date"`
	PlaceName string  `json:"place_name" binding:"required"`
	Latitude  float64 `json:"latitude" binding:"required"`
	Longitude float64 `json:"longitude" binding:"required"`
	Source    string  `json:"source" binding:"required"`
	Note      string  `json:"note"`
}

// AddPersonLocation godoc
// @Summary 新增人物地点轨迹
// @Description 为指定人物添加一条地点轨迹记录。
// @Tags timeline
// @Accept json
// @Produce json
// @Param id path string true "人物 UUID"
// @Param input body CreateLocationInput true "地点输入"
// @Success 201 {object} PersonLocationResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/persons/{id}/locations [post]
func AddPersonLocation(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		personID := c.Param("id")
		var person model.TimelinePerson

		if err := db.First(&person, "id = ?", personID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
			return
		}

		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		if person.UserID != userID.(uuid.UUID) && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		var input CreateLocationInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		date, err := parseDateTime(input.Date)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date format"})
			return
		}

		pid, _ := uuid.Parse(personID)
		location := model.PersonLocation{
			PersonID:  pid,
			Date:      date,
			PlaceName: input.PlaceName,
			Latitude:  input.Latitude,
			Longitude: input.Longitude,
			Source:    input.Source,
			Note:      input.Note,
		}

		if input.EndDate != "" {
			if endDate, err := parseDateTime(input.EndDate); err == nil {
				location.EndDate = &endDate
			}
		}

		if err := db.Create(&location).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create location"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": location})
	}
}

// UpdatePersonLocation godoc
// @Summary 更新人物地点轨迹
// @Description 人物作者或管理员可以更新地点轨迹记录。
// @Tags timeline
// @Accept json
// @Produce json
// @Param id path string true "地点记录 UUID"
// @Param input body CreateLocationInput true "地点输入"
// @Success 200 {object} PersonLocationResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/locations/{id} [put]
func UpdatePersonLocation(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var location model.PersonLocation

		if err := db.First(&location, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Location not found"})
			return
		}

		var person model.TimelinePerson
		db.First(&person, "id = ?", location.PersonID)

		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		if person.UserID != userID.(uuid.UUID) && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		var input CreateLocationInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		date, err := parseDateTime(input.Date)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date format"})
			return
		}

		updates := map[string]interface{}{
			"date":       date,
			"place_name": input.PlaceName,
			"latitude":   input.Latitude,
			"longitude":  input.Longitude,
			"source":     input.Source,
			"note":       input.Note,
		}

		if input.EndDate != "" {
			if endDate, err := parseDateTime(input.EndDate); err == nil {
				updates["end_date"] = endDate
			}
		} else {
			updates["end_date"] = nil
		}

		if err := db.Model(&location).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update location"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": location})
	}
}

// DeletePersonLocation godoc
// @Summary 删除人物地点轨迹
// @Description 人物作者或管理员可以删除地点轨迹记录。
// @Tags timeline
// @Produce json
// @Param id path string true "地点记录 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/locations/{id} [delete]
func DeletePersonLocation(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var location model.PersonLocation

		if err := db.First(&location, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Location not found"})
			return
		}

		var person model.TimelinePerson
		db.First(&person, "id = ?", location.PersonID)

		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		if person.UserID != userID.(uuid.UUID) && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		db.Delete(&location)
		c.JSON(http.StatusOK, gin.H{"message": "Location deleted"})
	}
}

// saveEventRevision saves a snapshot of the given event as a TimelineRevision.
func saveEventRevision(db *gorm.DB, event model.TimelineEvent, editorID uuid.UUID) {
	endDate := ""
	if event.EndDate != nil {
		endDate = event.EndDate.Format("2006-01-02")
	}
	rev := model.TimelineRevision{
		EventID:     event.ID,
		EditorID:    editorID,
		Title:       event.Title,
		Description: event.Description,
		Content:     event.Content,
		EventDate:   event.EventDate.Format("2006-01-02"),
		EndDate:     endDate,
		Location:    event.Location,
		Latitude:    event.Latitude,
		Longitude:   event.Longitude,
		Source:      event.Source,
		Category:    event.Category,
		Tags:        append(pq.StringArray(nil), event.Tags...),
		IsPublic:    event.IsPublic,
	}
	db.Create(&rev)
}

// GetTimelineEventHistory returns revision history for an event.
// Route: GET /api/timeline/events/:id/history
// GetTimelineEventHistory godoc
// @Summary 获取事件修订历史
// @Description 返回时间线事件的修订快照列表。
// @Tags timeline
// @Produce json
// @Param id path string true "事件 UUID"
// @Success 200 {object} TimelineRevisionListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/events/{id}/history [get]
func GetTimelineEventHistory(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var revisions []model.TimelineRevision
		if err := db.Preload("Editor").Where("event_id = ?", id).Order("created_at DESC").Find(&revisions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch history"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": revisions})
	}
}

// RevertTimelineEvent reverts an event to a specific revision (admin only).
// Route: POST /api/timeline/events/:id/revert/:revision_id
// RevertTimelineEvent godoc
// @Summary 回滚时间线事件
// @Description 管理员将时间线事件回滚到指定修订版本。
// @Tags timeline
// @Produce json
// @Param id path string true "事件 UUID"
// @Param revision_id path string true "修订 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/events/{id}/revert/{revision_id} [post]
func RevertTimelineEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin only"})
			return
		}

		id := c.Param("id")
		revID := c.Param("revision_id")

		var rev model.TimelineRevision
		if err := db.First(&rev, "id = ? AND event_id = ?", revID, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Revision not found"})
			return
		}

		eventDate, _ := parseDateTime(rev.EventDate)
		updates := map[string]interface{}{
			"title":       rev.Title,
			"description": rev.Description,
			"content":     rev.Content,
			"event_date":  eventDate,
			"location":    rev.Location,
			"latitude":    rev.Latitude,
			"longitude":   rev.Longitude,
			"source":      rev.Source,
			"category":    rev.Category,
			"tags":        rev.Tags,
			"is_public":   rev.IsPublic,
		}
		if rev.EndDate != "" {
			if endDate, err := parseDateTime(rev.EndDate); err == nil {
				updates["end_date"] = endDate
			}
		} else {
			updates["end_date"] = nil
		}

		if err := db.Model(&model.TimelineEvent{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revert event"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "reverted"})
	}
}
