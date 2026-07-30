package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
)

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
