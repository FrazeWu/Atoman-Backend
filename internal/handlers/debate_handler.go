package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
)

func SetupDebateRoutes(router *gin.Engine, db *gorm.DB) {
	debate := router.Group("/api/debate")
	{
		debate.GET("/topics", GetDebateTopics(db))
		debate.GET("/topics/:id", GetDebateTopic(db))
		debate.GET("/topics/:id/arguments", middleware.OptionalAuthMiddleware(), GetDebateArguments(db))
		debate.GET("/topics/search", SearchDebateTopics(db))

		protected := debate.Group("")
		protected.Use(middleware.AuthMiddleware())
		{
			// Topic CRUD
			protected.POST("/topics", CreateDebateTopic(db))
			protected.PUT("/topics/:id", UpdateDebateTopic(db))
			protected.DELETE("/topics/:id", DeleteDebateTopic(db))
			protected.POST("/topics/:id/conclude", ConcludeDebateTopic(db))
			protected.POST("/topics/:id/reopen", ReopenDebateTopic(db))
			protected.POST("/topics/:id/conclude-vote", VoteToConclude(db))

			// Argument CRUD
			protected.POST("/topics/:id/arguments", CreateArgument(db))
			protected.PUT("/arguments/:id", UpdateArgument(db))
			protected.DELETE("/arguments/:id", DeleteArgument(db))

			// Argument references (argument-to-argument)
			protected.POST("/arguments/:id/reference", AddArgumentReference(db))
			protected.DELETE("/arguments/:id/reference/:ref_id", RemoveArgumentReference(db))

			// Argument debate references (argument-to-debate-topic)
			protected.POST("/arguments/:id/debate-reference", AddDebateReference(db))
			protected.DELETE("/arguments/:id/debate-reference/:debate_id", RemoveDebateReference(db))

			// Voting
			protected.POST("/arguments/:id/vote", VoteArgument(db))
			protected.DELETE("/arguments/:id/vote", RemoveVote(db))
			protected.GET("/arguments/:id/votes", GetArgumentVotes(db)) // admin only

			// Admin fold/unfold
			protected.POST("/arguments/:id/fold", FoldArgument(db))
			protected.DELETE("/arguments/:id/fold", UnfoldArgument(db))
		}
	}
}

// ====== Topic Handlers ======

// GetDebateTopics godoc
// @Summary 获取辩题列表
// @Description 分页返回辩题列表，支持按状态和标签筛选。
// @Tags debate
// @Produce json
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Param status query string false "状态"
// @Param tag query string false "标签"
// @Success 200 {object} DebateListResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/debate/topics [get]
func GetDebateTopics(db *gorm.DB) gin.HandlerFunc {
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

		status := c.Query("status")
		tag := c.Query("tag")

		query := db.Model(&model.Debate{}).Preload("User")

		if status != "" {
			query = query.Where("status = ?", status)
		}
		if tag != "" {
			query = query.Where("tags @> ?", []string{tag})
		}

		var total int64
		query.Count(&total)

		var debates []model.Debate
		if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&debates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch debates"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data":  debates,
			"total": total,
			"page":  page,
			"limit": limit,
		})
	}
}

// GetDebateTopic godoc
// @Summary 获取辩题详情
// @Description 返回单个辩题详情，并增加一次浏览计数。
// @Tags debate
// @Produce json
// @Param id path string true "辩题 UUID"
// @Success 200 {object} DebateResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/debate/topics/{id} [get]
func GetDebateTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var debate model.Debate

		if err := db.Preload("User").First(&debate, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Debate not found"})
			return
		}

		// Increment view count
		db.Model(&debate).Update("view_count", debate.ViewCount+1)

		c.JSON(http.StatusOK, gin.H{"data": debate})
	}
}

type CreateDebateInput struct {
	Title       string   `json:"title" binding:"required"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
}

// CreateDebateTopic godoc
// @Summary 创建辩题
// @Description 创建一个新的辩题。
// @Tags debate
// @Accept json
// @Produce json
// @Param input body CreateDebateInput true "辩题输入"
// @Success 201 {object} DebateResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/topics [post]
func CreateDebateTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input CreateDebateInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID, _ := c.Get("user_id")
		debate := model.Debate{
			UserID:      userID.(uuid.UUID),
			Title:       input.Title,
			Description: input.Description,
			Content:     input.Content,
			Tags:        input.Tags,
			Status:      "open",
		}

		if err := db.Create(&debate).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create debate"})
			return
		}

		// Preload user for response
		db.Preload("User").First(&debate, debate.ID)

		c.JSON(http.StatusCreated, gin.H{"data": debate})
	}
}

// UpdateDebateTopic godoc
// @Summary 更新辩题
// @Description 辩题作者或管理员可以更新辩题内容。
// @Tags debate
// @Accept json
// @Produce json
// @Param id path string true "辩题 UUID"
// @Param input body CreateDebateInput true "辩题输入"
// @Success 200 {object} DebateResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/topics/{id} [put]
func UpdateDebateTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var debate model.Debate

		if err := db.First(&debate, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Debate not found"})
			return
		}

		// Check ownership or admin
		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		if debate.UserID != userID.(uuid.UUID) && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		var input CreateDebateInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]interface{}{
			"title":       input.Title,
			"description": input.Description,
			"content":     input.Content,
			"tags":        input.Tags,
		}

		if err := db.Model(&debate).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update debate"})
			return
		}

		db.Preload("User").First(&debate, debate.ID)
		c.JSON(http.StatusOK, gin.H{"data": debate})
	}
}

// DeleteDebateTopic godoc
// @Summary 删除辩题
// @Description 辩题作者或管理员可以删除辩题及其论点。
// @Tags debate
// @Produce json
// @Param id path string true "辩题 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/topics/{id} [delete]
func DeleteDebateTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var debate model.Debate

		if err := db.First(&debate, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Debate not found"})
			return
		}

		// Check ownership or admin
		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		if debate.UserID != userID.(uuid.UUID) && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		// Cascade delete arguments and votes
		db.Where("debate_id = ?", id).Delete(&model.Argument{})
		db.Delete(&debate)

		c.JSON(http.StatusOK, gin.H{"message": "Debate deleted"})
	}
}

// ConcludeDebateTopic godoc
// @Summary 结束辩题
// @Description 辩题作者或管理员给出结论并结束辩题。
// @Tags debate
// @Accept json
// @Produce json
// @Param id path string true "辩题 UUID"
// @Param input body DebateConcludeInput true "结论输入"
// @Success 200 {object} DebateResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/topics/{id}/conclude [post]
func ConcludeDebateTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var debate model.Debate

		if err := db.First(&debate, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Debate not found"})
			return
		}

		// Check ownership or admin
		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		if debate.UserID != userID.(uuid.UUID) && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		type ConcludeInput struct {
			ConclusionType    string `json:"conclusion_type" binding:"required,oneof=yes no inconclusive"`
			ConclusionSummary string `json:"conclusion_summary"`
		}
		var input ConcludeInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		now := time.Now()
		if err := db.Model(&debate).Updates(map[string]interface{}{
			"status":             "concluded",
			"concluded_at":       now,
			"conclusion_type":    input.ConclusionType,
			"conclusion_summary": input.ConclusionSummary,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to conclude debate"})
			return
		}

		db.Preload("User").First(&debate, debate.ID)
		c.JSON(http.StatusOK, gin.H{"data": debate})
	}
}

// ReopenDebateTopic godoc
// @Summary 重新开启辩题
// @Description 管理员重新开启已结束的辩题。
// @Tags debate
// @Produce json
// @Param id path string true "辩题 UUID"
// @Success 200 {object} DebateResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/topics/{id}/reopen [post]
func ReopenDebateTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var debate model.Debate

		if err := db.First(&debate, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Debate not found"})
			return
		}

		// Admin only
		role, _ := c.Get("role")
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin only"})
			return
		}

		if err := db.Model(&debate).Updates(map[string]interface{}{
			"status":             "open",
			"concluded_at":       nil,
			"conclusion_type":    "",
			"conclusion_summary": "",
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reopen debate"})
			return
		}

		db.Preload("User").First(&debate, debate.ID)
		c.JSON(http.StatusOK, gin.H{"data": debate})
	}
}

// VoteToConclude godoc
// @Summary 投票结束辩题
// @Description 对开放中的辩题投票要求结束，达到阈值时自动结束。
// @Tags debate
// @Produce json
// @Param id path string true "辩题 UUID"
// @Success 200 {object} DebateConcludeVoteResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/topics/{id}/conclude-vote [post]
func VoteToConclude(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var debate model.Debate

		if err := db.First(&debate, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Debate not found"})
			return
		}

		if debate.Status != "open" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Debate is not open"})
			return
		}

		userID, _ := c.Get("user_id")
		userUUID := userID.(uuid.UUID)

		// Check if user already voted to conclude
		var existing model.DebateConcludeVote
		if err := db.Where("debate_id = ? AND user_id = ?", debate.ID, userUUID).First(&existing).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "Already voted to conclude"})
			return
		}

		vote := model.DebateConcludeVote{
			DebateID: debate.ID,
			UserID:   userUUID,
		}
		if err := db.Create(&vote).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record vote"})
			return
		}

		newCount := debate.ConcludeVoteCount + 1
		db.Model(&debate).Update("conclude_vote_count", newCount)

		// Auto-conclude if threshold reached
		if newCount >= debate.ConcludeThreshold {
			now := time.Now()
			db.Model(&debate).Updates(map[string]interface{}{
				"status":          "concluded",
				"concluded_at":    now,
				"conclusion_type": "inconclusive",
			})
		}

		db.First(&debate, debate.ID)
		c.JSON(http.StatusOK, gin.H{
			"conclude_vote_count": debate.ConcludeVoteCount,
			"conclude_threshold":  debate.ConcludeThreshold,
			"auto_concluded":      debate.Status == "concluded",
		})
	}
}

// SearchDebateTopics godoc
// @Summary 搜索辩题
// @Description 按标题或描述搜索辩题。
// @Tags debate
// @Produce json
// @Param q query string false "搜索词"
// @Param limit query int false "返回数量"
// @Success 200 {object} DebateSearchResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/debate/topics/search [get]
func SearchDebateTopics(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Query("q")
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
		if limit < 1 || limit > 50 {
			limit = 10
		}

		var debates []model.Debate
		query := db.Model(&model.Debate{}).Preload("User")
		if q != "" {
			query = query.Where("title ILIKE ? OR description ILIKE ?", "%"+q+"%", "%"+q+"%")
		}
		if err := query.Order("created_at DESC").Limit(limit).Find(&debates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to search debates"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": debates})
	}
}

// ====== Argument Handlers ======

// GetDebateArguments godoc
// @Summary 获取辩题论点列表
// @Description 返回辩题下的论点列表，并在已登录时附带当前用户投票映射。
// @Tags debate
// @Produce json
// @Param id path string true "辩题 UUID"
// @Success 200 {object} DebateArgumentListResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/debate/topics/{id}/arguments [get]
func GetDebateArguments(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		debateID := c.Param("id")

		var arguments []model.Argument
		if err := db.Where("debate_id = ?", debateID).
			Preload("User").
			Preload("References").
			Preload("ReferencedDebates").
			Order("created_at ASC").
			Find(&arguments).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch arguments"})
			return
		}

		// Inject user votes if authenticated
		userVotes := make(map[string]int)
		if userID, exists := c.Get("user_id"); exists {
			var votes []model.DebateVote
			db.Where("user_id = ? AND argument_id IN (?)",
				userID,
				db.Model(&model.Argument{}).Select("id").Where("debate_id = ?", debateID),
			).Find(&votes)
			for _, v := range votes {
				userVotes[v.ArgumentID.String()] = v.VoteType
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": arguments, "user_votes": userVotes})
	}
}

type CreateArgumentInput struct {
	Content       string             `json:"content" binding:"required"`
	ArgumentType  model.ArgumentType `json:"argument_type" binding:"required"`
	ParentID      *uuid.UUID         `json:"parent_id"`
	SourceURL     string             `json:"source_url"`
	SourceTitle   string             `json:"source_title"`
	SourceExcerpt string             `json:"source_excerpt"`
}

// CreateArgument godoc
// @Summary 创建论点
// @Description 为指定辩题创建一条论点或引用型论点。
// @Tags debate
// @Accept json
// @Produce json
// @Param id path string true "辩题 UUID"
// @Param input body CreateArgumentInput true "论点输入"
// @Success 201 {object} DebateArgumentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/topics/{id}/arguments [post]
func CreateArgument(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		debateID := c.Param("id")
		var input CreateArgumentInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Verify debate exists and is open
		var debate model.Debate
		if err := db.Where("id = ?", debateID).First(&debate).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Debate not found"})
			return
		}
		if debate.Status != "open" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Debate is closed"})
			return
		}

		userID, _ := c.Get("user_id")
		if input.ParentID != nil {
			var quoted model.Argument
			if err := db.Select("id").First(&quoted, "id = ? AND debate_id = ?", *input.ParentID, debate.ID).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Quoted argument not found"})
				return
			}
		}

		argument := model.Argument{
			DebateID:      debate.ID,
			ParentID:      input.ParentID,
			UserID:        userID.(uuid.UUID),
			Content:       input.Content,
			ArgumentType:  input.ArgumentType,
			SourceURL:     input.SourceURL,
			SourceTitle:   input.SourceTitle,
			SourceExcerpt: input.SourceExcerpt,
		}

		if err := db.Create(&argument).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create argument"})
			return
		}

		// Update debate argument count
		db.Model(&debate).Update("argument_count", debate.ArgumentCount+1)

		db.Preload("User").Preload("References").Where("id = ?", argument.ID).First(&argument)
		c.JSON(http.StatusCreated, gin.H{"data": argument})
	}
}

// UpdateArgument godoc
// @Summary 更新论点
// @Description 论点作者可以更新自己的论点内容与来源信息。
// @Tags debate
// @Accept json
// @Produce json
// @Param id path string true "论点 UUID"
// @Param input body CreateArgumentInput true "论点输入"
// @Success 200 {object} DebateArgumentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/arguments/{id} [put]
func UpdateArgument(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var argument model.Argument

		if err := db.First(&argument, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Argument not found"})
			return
		}

		// Check ownership
		userID, _ := c.Get("user_id")
		if argument.UserID != userID.(uuid.UUID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		var input CreateArgumentInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]interface{}{
			"content":        input.Content,
			"argument_type":  input.ArgumentType,
			"source_url":     input.SourceURL,
			"source_title":   input.SourceTitle,
			"source_excerpt": input.SourceExcerpt,
		}

		if err := db.Model(&argument).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update argument"})
			return
		}

		db.Preload("User").Preload("References").First(&argument, argument.ID)
		c.JSON(http.StatusOK, gin.H{"data": argument})
	}
}

// DeleteArgument godoc
// @Summary 删除论点
// @Description 论点作者或管理员可以删除论点，并清理关联投票与引用关系。
// @Tags debate
// @Produce json
// @Param id path string true "论点 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/arguments/{id} [delete]
func DeleteArgument(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var argument model.Argument

		if err := db.First(&argument, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Argument not found"})
			return
		}

		// Check ownership or admin
		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		if argument.UserID != userID.(uuid.UUID) && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
			return
		}

		db.Model(&model.Argument{}).Where("parent_id = ?", argument.ID).Update("parent_id", nil)
		db.Where("argument_id = ?", argument.ID).Delete(&model.DebateVote{})
		_ = db.Model(&argument).Association("References").Clear()
		_ = db.Model(&argument).Association("ReferencedDebates").Clear()
		db.Delete(&argument)
		db.Model(&model.Debate{}).Where("id = ?", argument.DebateID).
			UpdateColumn("argument_count", gorm.Expr("CASE WHEN argument_count > 0 THEN argument_count - 1 ELSE 0 END"))

		c.JSON(http.StatusOK, gin.H{"message": "Argument deleted"})
	}
}

// ====== Reference Handlers ======

type ReferenceInput struct {
	ReferenceID uuid.UUID `json:"reference_id" binding:"required"`
}

// AddArgumentReference godoc
// @Summary 添加论点引用
// @Description 为论点关联另一个论点作为引用。
// @Tags debate
// @Accept json
// @Produce json
// @Param id path string true "论点 UUID"
// @Param input body ReferenceInput true "引用输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/arguments/{id}/reference [post]
func AddArgumentReference(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		argumentID := c.Param("id")
		var input ReferenceInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var argument model.Argument
		if err := db.First(&argument, "id = ?", argumentID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Argument not found"})
			return
		}

		var refArgument model.Argument
		if err := db.First(&refArgument, "id = ?", input.ReferenceID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Reference argument not found"})
			return
		}

		if err := db.Model(&argument).Association("References").Append(&refArgument); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add reference"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Reference added"})
	}
}

// RemoveArgumentReference godoc
// @Summary 移除论点引用
// @Description 删除论点与被引用论点之间的关联。
// @Tags debate
// @Produce json
// @Param id path string true "论点 UUID"
// @Param ref_id path string true "被引用论点 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/arguments/{id}/reference/{ref_id} [delete]
func RemoveArgumentReference(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		argumentID := c.Param("id")
		refID := c.Param("ref_id")

		var argument model.Argument
		if err := db.First(&argument, "id = ?", argumentID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Argument not found"})
			return
		}

		var refArgument model.Argument
		if err := db.First(&refArgument, "id = ?", refID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Reference not found"})
			return
		}

		if err := db.Model(&argument).Association("References").Delete(&refArgument); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove reference"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Reference removed"})
	}
}

// ====== Debate Reference Handlers ======

type DebateReferenceInput struct {
	DebateID uuid.UUID `json:"debate_id" binding:"required"`
}

// AddDebateReference godoc
// @Summary 添加辩题引用
// @Description 为论点关联另一个辩题作为参考。
// @Tags debate
// @Accept json
// @Produce json
// @Param id path string true "论点 UUID"
// @Param input body DebateReferenceInput true "辩题引用输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/arguments/{id}/debate-reference [post]
func AddDebateReference(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		argumentID := c.Param("id")
		var input DebateReferenceInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var argument model.Argument
		if err := db.First(&argument, "id = ?", argumentID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Argument not found"})
			return
		}

		var debate model.Debate
		if err := db.First(&debate, "id = ?", input.DebateID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Debate not found"})
			return
		}

		if err := db.Model(&argument).Association("ReferencedDebates").Append(&debate); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add debate reference"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Debate reference added"})
	}
}

// RemoveDebateReference godoc
// @Summary 移除辩题引用
// @Description 删除论点与被引用辩题之间的关联。
// @Tags debate
// @Produce json
// @Param id path string true "论点 UUID"
// @Param debate_id path string true "被引用辩题 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/arguments/{id}/debate-reference/{debate_id} [delete]
func RemoveDebateReference(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		argumentID := c.Param("id")
		debateID := c.Param("debate_id")

		var argument model.Argument
		if err := db.First(&argument, "id = ?", argumentID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Argument not found"})
			return
		}

		var debate model.Debate
		if err := db.First(&debate, "id = ?", debateID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Debate not found"})
			return
		}

		if err := db.Model(&argument).Association("ReferencedDebates").Delete(&debate); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove debate reference"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Debate reference removed"})
	}
}

// ====== Voting Handlers ======

type VoteInput struct {
	VoteType int `json:"vote_type" binding:"required,oneof=1 -1"`
}

// VoteArgument godoc
// @Summary 为论点投票
// @Description 对论点进行赞成或反对投票；再次提交相同票型会取消投票。
// @Tags debate
// @Accept json
// @Produce json
// @Param id path string true "论点 UUID"
// @Param input body VoteInput true "投票输入"
// @Success 200 {object} DebateArgumentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/arguments/{id}/vote [post]
func VoteArgument(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		argumentID := c.Param("id")
		var input VoteInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var argument model.Argument
		if err := db.First(&argument, "id = ?", argumentID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Argument not found"})
			return
		}

		userID, _ := c.Get("user_id")
		userUUID := userID.(uuid.UUID)

		// Check if user already voted
		var existingVote model.DebateVote
		result := db.Where("argument_id = ? AND user_id = ?", argumentID, userUUID).First(&existingVote)

		if result.Error == nil {
			// Update existing vote
			if existingVote.VoteType == input.VoteType {
				// Same vote, remove it
				db.Delete(&existingVote)
				db.Model(&argument).Update("vote_count", argument.VoteCount-existingVote.VoteType)
			} else {
				// Different vote, update and record history
				oldVoteType := existingVote.VoteType
				db.Model(&existingVote).Update("vote_type", input.VoteType)
				db.Model(&argument).Update("vote_count", argument.VoteCount-oldVoteType+input.VoteType)

				// Record history
				history := model.VoteHistory{
					ArgumentID:  argument.ID,
					UserID:      userUUID,
					OldVoteType: oldVoteType,
					NewVoteType: input.VoteType,
				}
				db.Create(&history)
			}
		} else {
			// Create new vote
			vote := model.DebateVote{
				ArgumentID: argument.ID,
				UserID:     userUUID,
				VoteType:   input.VoteType,
			}
			db.Create(&vote)
			db.Model(&argument).Update("vote_count", argument.VoteCount+input.VoteType)
		}

		db.Preload("User").First(&argument, argument.ID)
		c.JSON(http.StatusOK, gin.H{"data": argument})
	}
}

// RemoveVote godoc
// @Summary 取消论点投票
// @Description 删除当前用户对某条论点的投票。
// @Tags debate
// @Produce json
// @Param id path string true "论点 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/arguments/{id}/vote [delete]
func RemoveVote(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		argumentID := c.Param("id")
		userID, _ := c.Get("user_id")
		userUUID := userID.(uuid.UUID)

		var vote model.DebateVote
		if err := db.Where("argument_id = ? AND user_id = ?", argumentID, userUUID).First(&vote).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Vote not found"})
			return
		}

		var argument model.Argument
		db.First(&argument, argumentID)

		db.Delete(&vote)
		db.Model(&argument).Update("vote_count", argument.VoteCount-vote.VoteType)

		c.JSON(http.StatusOK, gin.H{"message": "Vote removed"})
	}
}

// GetArgumentVotes godoc
// @Summary 获取论点投票明细
// @Description 管理员查看某条论点的实名投票明细。
// @Tags debate
// @Produce json
// @Param id path string true "论点 UUID"
// @Success 200 {object} DebateVoteListResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/arguments/{id}/votes [get]
func GetArgumentVotes(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Admin only
		role, _ := c.Get("role")
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin only"})
			return
		}

		argumentID := c.Param("id")
		var votes []model.DebateVote
		if err := db.Where("argument_id = ?", argumentID).
			Preload("User").
			Order("created_at DESC").
			Find(&votes).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch votes"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": votes})
	}
}

// FoldArgument hides an argument from display (admin only).
// Route: POST /api/debate/arguments/:id/fold
// FoldArgument godoc
// @Summary 折叠论点
// @Description 管理员折叠一条论点并可记录折叠原因。
// @Tags debate
// @Accept json
// @Produce json
// @Param id path string true "论点 UUID"
// @Param input body FoldArgumentInput false "折叠备注"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/arguments/{id}/fold [post]
func FoldArgument(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin only"})
			return
		}
		id := c.Param("id")
		var req struct {
			FoldNote string `json:"fold_note"`
		}
		c.ShouldBindJSON(&req)
		if err := db.Model(&model.Argument{}).Where("id = ?", id).Updates(map[string]interface{}{
			"is_folded": true,
			"fold_note": req.FoldNote,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fold"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "folded"})
	}
}

// UnfoldArgument makes a previously folded argument visible again (admin only).
// Route: DELETE /api/debate/arguments/:id/fold
// UnfoldArgument godoc
// @Summary 取消折叠论点
// @Description 管理员恢复一条已折叠论点的显示状态。
// @Tags debate
// @Produce json
// @Param id path string true "论点 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/debate/arguments/{id}/fold [delete]
func UnfoldArgument(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin only"})
			return
		}
		id := c.Param("id")
		if err := db.Model(&model.Argument{}).Where("id = ?", id).Updates(map[string]interface{}{
			"is_folded": false,
			"fold_note": "",
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unfold"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "unfolded"})
	}
}
