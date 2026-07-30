package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"atoman/internal/model"
)

// ====== Event Handlers ======

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
		if err := query.Order("event_date ASC, timeline_events.id ASC").Limit(limit).Offset(offset).Find(&events).Error; err != nil {
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
