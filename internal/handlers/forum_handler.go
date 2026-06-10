package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/collab"
	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/service"
)

type forumHandler struct {
	db       *gorm.DB
	notifSvc *service.NotificationService
	userHub  *collab.UserHub
}

func SetupForumRoutes(router *gin.Engine, db *gorm.DB, notifSvc *service.NotificationService, userHub *collab.UserHub) {
	forum := router.Group("/api/v1/forum")
	{
		forum.GET("/search", SearchForumTopics(db))

		protected := forum.Group("")
		protected.Use(middleware.AuthMiddleware())
		{
			protected.POST("/topics/:topicID/close", CloseForumTopic(db))
			protected.POST("/replies/:replyID/like", (&forumHandler{db: db, notifSvc: notifSvc, userHub: userHub}).ToggleForumReplyLike())
			protected.POST("/replies/:replyID/solve", (&forumHandler{db: db, notifSvc: notifSvc, userHub: userHub}).SolveForumReply())
			protected.DELETE("/replies/:replyID/solve", UnsolveForumReply(db))
			protected.POST("/topics/:topicID/feature", FeatureForumTopic(db))
			protected.DELETE("/topics/:topicID/feature", UnfeatureForumTopic(db))
			protected.POST("/report", ReportForumContent(db))
			protected.POST("/category-requests", CreateCategoryRequest(db))
			protected.GET("/category-requests", GetCategoryRequests(db))
			protected.POST("/category-requests/:requestID/review", ReviewCategoryRequest(db))
		}
	}
}

// ─── Categories ────────────────────────────────────────────────────────────────

// GetForumCategories godoc
// @Summary 获取论坛分类列表
// @Description 返回全部论坛分类以及每个分类的话题数量。
// @Tags forum
// @Produce json
// @Success 200 {object} ForumCategoryListResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/forum/categories [get]
func GetForumCategories(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var categories []model.ForumCategory
		if err := db.Find(&categories).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch categories"})
			return
		}
		for i := range categories {
			var count int64
			db.Model(&model.ForumTopic{}).Where("category_id = ? AND deleted_at IS NULL", categories[i].ID).Count(&count)
			categories[i].TopicCount = int(count)
		}
		c.JSON(http.StatusOK, gin.H{"data": categories})
	}
}

// CreateForumCategory godoc
// @Summary 创建论坛分类
// @Description 仅管理员可创建论坛分类。
// @Tags forum
// @Accept json
// @Produce json
// @Param input body ForumCategoryInput true "分类输入"
// @Success 201 {object} ForumCategoryResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/categories [post]
func CreateForumCategory(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin only"})
			return
		}
		var input struct {
			Name        string `json:"name" binding:"required"`
			Description string `json:"description"`
			Color       string `json:"color"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		cat := model.ForumCategory{Name: input.Name, Description: input.Description, Color: input.Color}
		if cat.Color == "" {
			cat.Color = "#000000"
		}
		if err := db.Create(&cat).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create category"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": cat})
	}
}

// ─── Topics ────────────────────────────────────────────────────────────────────

// GetForumTopics godoc
// @Summary 获取论坛话题列表
// @Description 返回论坛话题分页列表，支持分类、标签、搜索和排序筛选。
// @Tags forum
// @Produce json
// @Param page query int false "页码" default(1)
// @Param limit query int false "每页数量" default(20)
// @Param sort query string false "排序：latest/top/active/featured" default(latest)
// @Param category_id query string false "分类 UUID"
// @Param tag query string false "标签"
// @Param search query string false "搜索关键字"
// @Success 200 {object} ForumTopicListResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/forum/topics [get]
func GetForumTopics(db *gorm.DB) gin.HandlerFunc {
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

		sort := c.DefaultQuery("sort", "latest")
		categoryID := c.Query("category_id")
		tag := c.Query("tag")
		search := c.Query("search")

		query := db.Model(&model.ForumTopic{}).Preload("User").Preload("Category")
		if categoryID != "" {
			query = query.Where("category_id = ?", categoryID)
		}
		if tag != "" {
			dialect := db.Dialector.Name()
			if dialect == "postgres" || dialect == "pgx" {
				query = query.Where("? = ANY(tags)", tag)
			} else {
				query = query.Where("tags LIKE ?", "%"+tag+"%")
			}
		}
		if search != "" {
			query = query.Where("title ILIKE ? OR content ILIKE ?",
				"%"+search+"%", "%"+search+"%")
		}

		var total int64
		query.Count(&total)

		orderClause := "pinned DESC, created_at DESC"
		if sort == "top" {
			orderClause = "pinned DESC, like_count DESC, reply_count DESC"
		} else if sort == "active" {
			orderClause = "pinned DESC, COALESCE(last_reply_at, created_at) DESC"
		} else if sort == "featured" {
			query = query.Where("featured = ?", true)
			orderClause = "created_at DESC"
		}

		var topics []model.ForumTopic
		if err := query.Order(orderClause).Limit(limit).Offset(offset).Find(&topics).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch topics"})
			return
		}

		enrichTopicsForUser(db, c, topics)

		c.JSON(http.StatusOK, gin.H{
			"data":  topics,
			"total": total,
			"page":  page,
			"limit": limit,
		})
	}
}

// GetForumTopic godoc
// @Summary 获取论坛话题详情
// @Description 返回指定话题详情，并在允许时更新浏览量与当前用户状态。
// @Tags forum
// @Produce json
// @Param id path string true "话题 UUID"
// @Success 200 {object} ForumTopicResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/forum/topics/{id} [get]
func GetForumTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid topic id"})
			return
		}
		var topic model.ForumTopic
		if err := db.Preload("User").Preload("Category").First(&topic, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Topic not found"})
			return
		}

		// Increment view count (deduplicated per authenticated user per hour)
		currentUserID, hasUser := c.Get("user_id")
		if hasUser {
			uid, _ := currentUserID.(uuid.UUID)
			var recentView int64
			db.Model(&model.ActivityLog{}).
				Where("user_id = ? AND action = 'view_topic' AND target_id = ? AND created_at > ?",
					uid, topic.ID, time.Now().Add(-1*time.Hour)).
				Count(&recentView)
			if recentView == 0 {
				db.Model(&topic).UpdateColumn("view_count", gorm.Expr("view_count + 1"))
				topic.ViewCount++
				service.LogActivity(db, uid, "view_topic", "topic", topic.ID)
			}
			// Set like / bookmark status
			var like model.ForumLike
			if db.Where("user_id = ? AND target_type = ? AND target_id = ?", uid, "topic", topic.ID).First(&like).Error == nil {
				topic.IsLiked = true
			}
			var bookmark model.ForumBookmark
			if db.Where("user_id = ? AND topic_id = ?", uid, topic.ID).First(&bookmark).Error == nil {
				topic.IsBookmarked = true
			}
		} else {
			// Anonymous: always increment
			db.Model(&topic).UpdateColumn("view_count", gorm.Expr("view_count + 1"))
			topic.ViewCount++
		}

		c.JSON(http.StatusOK, gin.H{"data": topic})
	}
}

// CreateForumTopic godoc
// @Summary 创建论坛话题
// @Description 创建一个新的论坛话题。
// @Tags forum
// @Accept json
// @Produce json
// @Param input body ForumTopicCreateInput true "话题输入"
// @Success 201 {object} ForumTopicResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/topics [post]
func CreateForumTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			CategoryID string   `json:"category_id" binding:"required"`
			Title      string   `json:"title" binding:"required"`
			Content    string   `json:"content" binding:"required"`
			Tags       []string `json:"tags"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		catID, err := uuid.Parse(input.CategoryID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid category_id"})
			return
		}
		var cat model.ForumCategory
		if db.First(&cat, "id = ?", catID).Error != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Category not found"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)

		tags := model.StringSlice(input.Tags)
		if tags == nil {
			tags = model.StringSlice{}
		}

		topic := model.ForumTopic{
			UserID:     uid,
			CategoryID: catID,
			Title:      input.Title,
			Content:    input.Content,
			Tags:       tags,
		}
		if err := db.Create(&topic).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create topic"})
			return
		}
		db.Preload("User").Preload("Category").First(&topic, "id = ?", topic.ID)

		// Log activity
		service.LogActivity(db, uid, "create_topic", "topic", topic.ID)

		c.JSON(http.StatusCreated, gin.H{"data": topic})
	}
}

// UpdateForumTopic godoc
// @Summary 更新论坛话题
// @Description 更新当前用户拥有的话题，管理员也可更新。
// @Tags forum
// @Accept json
// @Produce json
// @Param id path string true "话题 UUID"
// @Param input body ForumTopicUpdateInput true "话题更新输入"
// @Success 200 {object} ForumTopicResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/topics/{id} [put]
func UpdateForumTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid topic id"})
			return
		}
		var topic model.ForumTopic
		if db.First(&topic, "id = ?", id).Error != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Topic not found"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)
		role, _ := c.Get("role")
		if topic.UserID != uid && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
			return
		}
		var input struct {
			Title   string   `json:"title"`
			Content string   `json:"content"`
			Tags    []string `json:"tags"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if input.Title != "" {
			topic.Title = input.Title
		}
		if input.Content != "" {
			topic.Content = input.Content
		}
		if input.Tags != nil {
			topic.Tags = model.StringSlice(input.Tags)
		}
		db.Save(&topic)
		c.JSON(http.StatusOK, gin.H{"data": topic})
	}
}

// DeleteForumTopic godoc
// @Summary 删除论坛话题
// @Description 删除当前用户拥有的话题，管理员也可删除。
// @Tags forum
// @Produce json
// @Param id path string true "话题 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/topics/{id} [delete]
func DeleteForumTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid topic id"})
			return
		}
		var topic model.ForumTopic
		if db.First(&topic, "id = ?", id).Error != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Topic not found"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)
		role, _ := c.Get("role")
		if topic.UserID != uid && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
			return
		}
		db.Delete(&topic)
		c.JSON(http.StatusOK, gin.H{"message": "Deleted"})
	}
}

// ToggleForumTopicLike godoc
// @Summary 点赞或取消点赞论坛话题
// @Description 再次调用会在点赞与取消点赞之间切换。
// @Tags forum
// @Produce json
// @Param id path string true "话题 UUID"
// @Success 200 {object} BoolStatusResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/topics/{id}/like [post]
func (h *forumHandler) ToggleForumTopicLike() gin.HandlerFunc {
	return func(c *gin.Context) {
		db := h.db
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid topic id"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)

		var topic model.ForumTopic
		if err := db.Select("id", "user_id", "title").First(&topic, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Topic not found"})
			return
		}

		var actor model.User
		db.Select("uuid", "username", "display_name").First(&actor, "uuid = ?", uid)

		var like model.ForumLike
		if db.Where("user_id = ? AND target_type = ? AND target_id = ?", uid, "topic", id).First(&like).Error == nil {
			db.Delete(&like)
			db.Model(&model.ForumTopic{}).Where("id = ?", id).UpdateColumn("like_count", gorm.Expr("like_count - 1"))
			c.JSON(http.StatusOK, gin.H{"liked": false})
		} else {
			db.Create(&model.ForumLike{UserID: uid, TargetType: "topic", TargetID: id})
			db.Model(&model.ForumTopic{}).Where("id = ?", id).UpdateColumn("like_count", gorm.Expr("like_count + 1"))
			if h.notifSvc != nil {
				_ = h.notifSvc.NotifyForumLike(topic.UserID, uid, getDisplayName(&actor), "forum_topic", topic.ID, topic.ID, topic.Title, WsPushNotif(h.userHub))
			}
			c.JSON(http.StatusOK, gin.H{"liked": true})
		}
	}
}

// ToggleForumTopicBookmark godoc
// @Summary 收藏或取消收藏论坛话题
// @Description 再次调用会在收藏与取消收藏之间切换。
// @Tags forum
// @Produce json
// @Param id path string true "话题 UUID"
// @Success 200 {object} BoolStatusResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/topics/{id}/bookmark [post]
func ToggleForumTopicBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid topic id"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)

		var bookmark model.ForumBookmark
		if db.Where("user_id = ? AND topic_id = ?", uid, id).First(&bookmark).Error == nil {
			db.Delete(&bookmark)
			c.JSON(http.StatusOK, gin.H{"bookmarked": false})
		} else {
			db.Create(&model.ForumBookmark{UserID: uid, TopicID: id})
			c.JSON(http.StatusOK, gin.H{"bookmarked": true})
		}
	}
}

// PinForumTopic godoc
// @Summary 切换话题置顶状态
// @Description 仅管理员可切换论坛话题的置顶状态。
// @Tags forum
// @Produce json
// @Param id path string true "话题 UUID"
// @Success 200 {object} BoolStatusResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/topics/{id}/pin [post]
func PinForumTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin only"})
			return
		}
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid topic id"})
			return
		}
		var topic model.ForumTopic
		if db.First(&topic, "id = ?", id).Error != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Topic not found"})
			return
		}
		topic.Pinned = !topic.Pinned
		db.Save(&topic)
		c.JSON(http.StatusOK, gin.H{"pinned": topic.Pinned})
	}
}

// CloseForumTopic godoc
// @Summary 切换话题关闭状态
// @Description 仅管理员可切换论坛话题的关闭状态。
// @Tags forum
// @Produce json
// @Param id path string true "话题 UUID"
// @Success 200 {object} BoolStatusResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/topics/{id}/close [post]
func CloseForumTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin only"})
			return
		}
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid topic id"})
			return
		}
		var topic model.ForumTopic
		if db.First(&topic, "id = ?", id).Error != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Topic not found"})
			return
		}
		topic.Closed = !topic.Closed
		db.Save(&topic)
		c.JSON(http.StatusOK, gin.H{"closed": topic.Closed})
	}
}

// ─── Replies ───────────────────────────────────────────────────────────────────

// GetForumReplies godoc
// @Summary 获取话题回复列表
// @Description 返回指定话题下的回复列表，支持按 oldest 或 best 排序。
// @Tags forum
// @Produce json
// @Param id path string true "话题 UUID"
// @Param sort query string false "排序：oldest/best" default(oldest)
// @Success 200 {object} ForumReplyListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/forum/topics/{id}/replies [get]
func GetForumReplies(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		topicID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid topic id"})
			return
		}

		sort := c.DefaultQuery("sort", "oldest") // oldest | best

		var allReplies []model.ForumReply
		query := db.Preload("User").Where("topic_id = ? AND deleted_at IS NULL", topicID)
		if sort == "best" {
			query = query.Order("like_count DESC, floor_number ASC")
		} else {
			query = query.Order("floor_number ASC")
		}
		if err := query.Find(&allReplies).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch replies"})
			return
		}

		// Enrich with per-user like status
		currentUserID, hasUser := c.Get("user_id")
		if hasUser {
			uid, _ := currentUserID.(uuid.UUID)
			replyIDs := make([]uuid.UUID, len(allReplies))
			for i, r := range allReplies {
				replyIDs[i] = r.ID
			}
			var likes []model.ForumLike
			db.Where("user_id = ? AND target_type = ? AND target_id IN ?", uid, "reply", replyIDs).Find(&likes)
			likedSet := map[uuid.UUID]bool{}
			for _, l := range likes {
				likedSet[l.TargetID] = true
			}
			for i := range allReplies {
				allReplies[i].IsLiked = likedSet[allReplies[i].ID]
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": allReplies})
	}
}

// CreateForumReply godoc
// @Summary 创建论坛回复
// @Description 为指定话题创建回复，可引用一条已有回复。
// @Tags forum
// @Accept json
// @Produce json
// @Param id path string true "话题 UUID"
// @Param input body ForumReplyCreateInput true "回复输入"
// @Success 201 {object} ForumReplyResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/topics/{id}/replies [post]
func (h *forumHandler) CreateForumReply() gin.HandlerFunc {
	return func(c *gin.Context) {
		db := h.db
		topicID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid topic id"})
			return
		}
		var topic model.ForumTopic
		if db.First(&topic, "id = ?", topicID).Error != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Topic not found"})
			return
		}
		if topic.Closed {
			c.JSON(http.StatusForbidden, gin.H{"error": "Topic is closed"})
			return
		}

		var input struct {
			Content       string  `json:"content" binding:"required"`
			ParentReplyID *string `json:"parent_reply_id"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)

		var parentUID *uuid.UUID
		if input.ParentReplyID != nil && *input.ParentReplyID != "" {
			pid, err := uuid.Parse(*input.ParentReplyID)
			if err == nil {
				parentUID = &pid
			}
		}

		var replyDepth int
		if parentUID != nil {
			var quotedReply model.ForumReply
			if err := db.Select("id", "depth").First(&quotedReply, "id = ? AND topic_id = ? AND deleted_at IS NULL", *parentUID, topicID).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Quoted reply not found"})
				return
			}
			if quotedReply.Depth >= 1 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "maximum reply nesting depth reached"})
				return
			}
			replyDepth = quotedReply.Depth + 1
		}

		// Calculate flat floor order; parent_reply_id is now quote metadata only.
		path, floor, err := service.BuildReplyPath(db, topicID, parentUID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to compute reply path"})
			return
		}

		reply := model.ForumReply{
			TopicID:       topicID,
			UserID:        uid,
			ParentReplyID: parentUID,
			Content:       input.Content,
			Path:          path,
			FloorNumber:   floor,
			Depth:         replyDepth,
		}
		if err := db.Create(&reply).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create reply"})
			return
		}

		// Update topic counters
		now := time.Now()
		db.Model(&topic).Updates(map[string]interface{}{
			"reply_count":   gorm.Expr("reply_count + 1"),
			"last_reply_at": now,
		})

		db.Preload("User").First(&reply, "id = ?", reply.ID)

		// 3. Log activity
		service.LogActivity(db, uid, "create_reply", "reply", reply.ID)

		if h.notifSvc != nil {
			_ = h.notifSvc.NotifyForumReply(&reply, &topic, WsPushNotif(h.userHub))
		}

		c.JSON(http.StatusCreated, gin.H{"data": reply})
	}
}

// UpdateForumReply godoc
// @Summary 更新论坛回复
// @Description 更新当前用户拥有的回复，管理员也可更新。
// @Tags forum
// @Accept json
// @Produce json
// @Param id path string true "回复 UUID"
// @Param input body ForumReplyUpdateInput true "回复更新输入"
// @Success 200 {object} ForumReplyResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/replies/{id} [put]
func UpdateForumReply(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid reply id"})
			return
		}
		var reply model.ForumReply
		if db.First(&reply, "id = ?", id).Error != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Reply not found"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)
		role, _ := c.Get("role")
		if reply.UserID != uid && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
			return
		}
		var input struct {
			Content string `json:"content" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		reply.Content = input.Content
		db.Save(&reply)
		c.JSON(http.StatusOK, gin.H{"data": reply})
	}
}

// DeleteForumReply godoc
// @Summary 删除论坛回复
// @Description 删除当前用户拥有的回复，管理员也可删除。
// @Tags forum
// @Produce json
// @Param id path string true "回复 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/replies/{id} [delete]
func DeleteForumReply(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid reply id"})
			return
		}
		var reply model.ForumReply
		if db.First(&reply, "id = ?", id).Error != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Reply not found"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)
		role, _ := c.Get("role")
		if reply.UserID != uid && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
			return
		}
		db.Model(&model.ForumReply{}).Where("parent_reply_id = ?", reply.ID).Update("parent_reply_id", nil)
		db.Delete(&reply)
		db.Model(&model.ForumTopic{}).Where("id = ?", reply.TopicID).
			UpdateColumn("reply_count", gorm.Expr("reply_count - 1"))
		c.JSON(http.StatusOK, gin.H{"message": "Deleted"})
	}
}

// ToggleForumReplyLike godoc
// @Summary 点赞或取消点赞回复
// @Description 再次调用会在点赞与取消点赞之间切换。
// @Tags forum
// @Produce json
// @Param id path string true "回复 UUID"
// @Success 200 {object} BoolStatusResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/replies/{id}/like [post]
func (h *forumHandler) ToggleForumReplyLike() gin.HandlerFunc {
	return func(c *gin.Context) {
		db := h.db
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid reply id"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)

		var like model.ForumLike
		if db.Where("user_id = ? AND target_type = ? AND target_id = ?", uid, "reply", id).First(&like).Error == nil {
			db.Delete(&like)
			db.Model(&model.ForumReply{}).Where("id = ?", id).UpdateColumn("like_count", gorm.Expr("like_count - 1"))
			c.JSON(http.StatusOK, gin.H{"liked": false})
		} else {
			db.Create(&model.ForumLike{UserID: uid, TargetType: "reply", TargetID: id})
			db.Model(&model.ForumReply{}).Where("id = ?", id).UpdateColumn("like_count", gorm.Expr("like_count + 1"))
			var replyOwner model.ForumReply
			if db.Preload("User").First(&replyOwner, "id = ?", id).Error == nil {
				if replyOwner.UserID != uid {
					service.LogActivity(db, replyOwner.UserID, "receive_like", "reply", id)
					if h.notifSvc != nil {
						var actor model.User
						db.Select("uuid", "username", "display_name").First(&actor, "uuid = ?", uid)
						var topic model.ForumTopic
						if db.Select("id", "title").First(&topic, "id = ?", replyOwner.TopicID).Error == nil {
							_ = h.notifSvc.NotifyForumLike(replyOwner.UserID, uid, getDisplayName(&actor), "forum_reply", replyOwner.ID, topic.ID, topic.Title, WsPushNotif(h.userHub))
						}
					}
				}
			}
			// Auto-solve: if like count reaches threshold, mark topic as solved
			var likeCount int64
			db.Model(&model.ForumLike{}).Where("target_type = 'reply' AND target_id = ?", id).Count(&likeCount)
			threshold := int64(10)
			var setting model.SiteSetting
			if db.Where("key = ?", "forum.solved_auto_threshold").First(&setting).Error == nil {
				if v, err := strconv.ParseInt(setting.Value, 10, 64); err == nil {
					threshold = v
				}
			}
			if likeCount >= threshold {
				var topic model.ForumTopic
				if db.First(&topic, "id = ?", replyOwner.TopicID).Error == nil && !topic.IsSolved {
					db.Model(&topic).Updates(map[string]interface{}{
						"is_solved":       true,
						"solved_reply_id": id,
					})
					db.Model(&model.ForumReply{}).Where("id = ?", id).Update("is_solved", true)
				}
			}
			c.JSON(http.StatusOK, gin.H{"liked": true})
		}
	}
}

// SolveForumReply marks a reply as the solution (topic owner or admin only).
// Route: POST /api/forum/replies/:id/solve
// SolveForumReply godoc
// @Summary 标记解决方案
// @Description 将指定回复标记为话题解决方案。
// @Tags forum
// @Produce json
// @Param id path string true "回复 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/replies/{id}/solve [post]
func (h *forumHandler) SolveForumReply() gin.HandlerFunc {
	return func(c *gin.Context) {
		db := h.db
		replyID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid reply id"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)

		var reply model.ForumReply
		if err := db.First(&reply, "id = ?", replyID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "reply not found"})
			return
		}

		var topic model.ForumTopic
		if err := db.First(&topic, "id = ?", reply.TopicID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "topic not found"})
			return
		}

		if topic.UserID != uid && !isAdmin(c) {
			c.JSON(http.StatusForbidden, gin.H{"error": "only topic owner or admin can mark solution"})
			return
		}

		if err := db.Model(&topic).Updates(map[string]interface{}{
			"is_solved":       true,
			"solved_reply_id": reply.ID,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed"})
			return
		}
		db.Model(&reply).Update("is_solved", true)
		if h.notifSvc != nil {
			_ = h.notifSvc.NotifyForumSolved(&reply, &topic, uid, WsPushNotif(h.userHub))
		}
		c.JSON(http.StatusOK, gin.H{"message": "solved"})
	}
}

// UnsolveForumReply removes the solution mark (topic owner or admin only).
// Route: DELETE /api/forum/replies/:id/solve
// UnsolveForumReply godoc
// @Summary 取消解决方案标记
// @Description 取消指定回复的话题解决方案状态。
// @Tags forum
// @Produce json
// @Param id path string true "回复 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/replies/{id}/solve [delete]
func UnsolveForumReply(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		replyID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid reply id"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)

		var reply model.ForumReply
		if err := db.First(&reply, "id = ?", replyID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "reply not found"})
			return
		}

		var topic model.ForumTopic
		if err := db.First(&topic, "id = ?", reply.TopicID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "topic not found"})
			return
		}

		if topic.UserID != uid && !isAdmin(c) {
			c.JSON(http.StatusForbidden, gin.H{"error": "only topic owner or admin can unmark solution"})
			return
		}

		db.Model(&topic).Updates(map[string]interface{}{
			"is_solved":       false,
			"solved_reply_id": nil,
		})
		db.Model(&reply).Update("is_solved", false)
		c.JSON(http.StatusOK, gin.H{"message": "unsolved"})
	}
}

// ─── Search ────────────────────────────────────────────────────────────────────

// SearchForumTopics godoc
// @Summary 搜索论坛话题
// @Description 按标题或正文搜索论坛话题。
// @Tags forum
// @Produce json
// @Param q query string false "搜索关键字"
// @Param page query int false "页码" default(1)
// @Param limit query int false "每页数量" default(20)
// @Success 200 {object} ForumSearchResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/forum/search [get]
func SearchForumTopics(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := strings.TrimSpace(c.Query("q"))
		if q == "" {
			c.JSON(http.StatusOK, gin.H{"data": []model.ForumTopic{}, "total": 0})
			return
		}
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if page < 1 {
			page = 1
		}
		if limit < 1 || limit > 100 {
			limit = 20
		}
		offset := (page - 1) * limit

		var topics []model.ForumTopic
		var total int64
		query := db.Model(&model.ForumTopic{}).Preload("User").Preload("Category").
			Where("(title ILIKE ? OR content ILIKE ?) AND deleted_at IS NULL",
				"%"+q+"%", "%"+q+"%")
		query.Count(&total)
		if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&topics).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Search failed"})
			return
		}
		enrichTopicsForUser(db, c, topics)
		c.JSON(http.StatusOK, gin.H{"data": topics, "total": total, "page": page, "limit": limit, "q": q})
	}
}

// ─── Drafts ────────────────────────────────────────────────────────────────────

// GetForumDraft godoc
// @Summary 获取论坛草稿
// @Description 按 context_key 获取当前用户的论坛草稿。
// @Tags forum
// @Produce json
// @Param context_key query string true "草稿上下文 key"
// @Success 200 {object} ForumDraftResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/drafts [get]
func GetForumDraft(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		contextKey := c.Query("context_key")
		if contextKey == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "context_key required"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)

		var draft model.ForumDraft
		if err := db.Where("user_id = ? AND context_key = ?", uid, contextKey).First(&draft).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Draft not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": draft})
	}
}

// PutForumDraft godoc
// @Summary 保存论坛草稿
// @Description 创建或更新当前用户的论坛草稿。
// @Tags forum
// @Accept json
// @Produce json
// @Param input body ForumDraftInput true "论坛草稿输入"
// @Success 200 {object} ForumDraftResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/drafts [put]
func PutForumDraft(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			ContextKey string `json:"context_key" binding:"required"`
			Title      string `json:"title"`
			Content    string `json:"content"`
			Tags       string `json:"tags"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)

		var draft model.ForumDraft
		result := db.Where("user_id = ? AND context_key = ?", uid, input.ContextKey).First(&draft)
		if result.Error != nil {
			// Create new
			draft = model.ForumDraft{
				UserID:     uid,
				ContextKey: input.ContextKey,
				Title:      input.Title,
				Content:    input.Content,
				Tags:       input.Tags,
			}
			db.Create(&draft)
		} else {
			// Update existing
			draft.Title = input.Title
			draft.Content = input.Content
			draft.Tags = input.Tags
			db.Save(&draft)
		}
		c.JSON(http.StatusOK, gin.H{"data": draft})
	}
}

// DeleteForumDraft godoc
// @Summary 删除论坛草稿
// @Description 按 context_key 删除当前用户的论坛草稿。
// @Tags forum
// @Produce json
// @Param context_key query string true "草稿上下文 key"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/drafts [delete]
func DeleteForumDraft(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		contextKey := c.Query("context_key")
		if contextKey == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "context_key required"})
			return
		}
		userID, _ := c.Get("user_id")
		uid, _ := userID.(uuid.UUID)
		db.Where("user_id = ? AND context_key = ?", uid, contextKey).Delete(&model.ForumDraft{})
		c.JSON(http.StatusOK, gin.H{"message": "Draft deleted"})
	}
}

// ─── Helpers ───────────────────────────────────────────────────────────────────

// enrichTopicsForUser bulk-loads like/bookmark status for the authenticated user.
func enrichTopicsForUser(db *gorm.DB, c *gin.Context, topics []model.ForumTopic) {
	currentUserID, hasUser := c.Get("user_id")
	if !hasUser || len(topics) == 0 {
		return
	}
	uid, _ := currentUserID.(uuid.UUID)

	topicIDs := make([]uuid.UUID, len(topics))
	for i, t := range topics {
		topicIDs[i] = t.ID
	}

	// Likes
	var likes []model.ForumLike
	db.Where("user_id = ? AND target_type = ? AND target_id IN ?", uid, "topic", topicIDs).Find(&likes)
	likedSet := map[uuid.UUID]bool{}
	for _, l := range likes {
		likedSet[l.TargetID] = true
	}

	// Bookmarks
	var bookmarks []model.ForumBookmark
	db.Where("user_id = ? AND topic_id IN ?", uid, topicIDs).Find(&bookmarks)
	bookmarkSet := map[uuid.UUID]bool{}
	for _, b := range bookmarks {
		bookmarkSet[b.TopicID] = true
	}

	for i := range topics {
		topics[i].IsLiked = likedSet[topics[i].ID]
		topics[i].IsBookmarked = bookmarkSet[topics[i].ID]
	}
}

// truncate shortens a string to maxLen runes.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// getDisplayName returns display_name or username for a User pointer.
func getDisplayName(u *model.User) string {
	if u == nil {
		return "匿名"
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// isAdmin checks whether the authenticated user has the admin role.
func isAdmin(c *gin.Context) bool {
	role, _ := c.Get("role")
	return role == "admin"
}

// FeatureForumTopic marks a topic as featured (admin only).
// Route: POST /api/forum/topics/:id/feature
// FeatureForumTopic godoc
// @Summary 设为精选话题
// @Description 仅管理员可将话题设为精选。
// @Tags forum
// @Produce json
// @Param id path string true "话题 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/topics/{id}/feature [post]
func FeatureForumTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isAdmin(c) {
			c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
			return
		}
		id := c.Param("id")
		if err := db.Model(&model.ForumTopic{}).Where("id = ?", id).Update("featured", true).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "featured"})
	}
}

// UnfeatureForumTopic removes featured status from a topic (admin only).
// Route: DELETE /api/forum/topics/:id/feature
// UnfeatureForumTopic godoc
// @Summary 取消精选话题
// @Description 仅管理员可取消话题的精选状态。
// @Tags forum
// @Produce json
// @Param id path string true "话题 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/topics/{id}/feature [delete]
func UnfeatureForumTopic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isAdmin(c) {
			c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
			return
		}
		id := c.Param("id")
		if err := db.Model(&model.ForumTopic{}).Where("id = ?", id).Update("featured", false).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "unfeatured"})
	}
}

// ReportForumContent submits a report for a topic or reply.
// Route: POST /api/forum/report
// Side effect: if topic report count >= 10, auto-close the topic.
// ReportForumContent godoc
// @Summary 举报论坛内容
// @Description 举报话题或回复；当话题举报数达到阈值时会自动关闭。
// @Tags forum
// @Accept json
// @Produce json
// @Param input body ForumReportInput true "举报输入"
// @Success 201 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/report [post]
func ReportForumContent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)

		var req struct {
			TargetType string `json:"target_type" binding:"required,oneof=topic reply"`
			TargetID   string `json:"target_id" binding:"required"`
			Reason     string `json:"reason" binding:"required,oneof=spam off-topic harassment other"`
			Note       string `json:"note"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		targetUUID, err := uuid.Parse(req.TargetID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid target_id"})
			return
		}

		// Prevent duplicate reports
		var existing model.ForumReport
		if db.Where("user_id = ? AND target_type = ? AND target_id = ?", userID, req.TargetType, targetUUID).First(&existing).Error == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "already reported"})
			return
		}

		report := model.ForumReport{
			UserID:     userID,
			TargetType: req.TargetType,
			TargetID:   targetUUID,
			Reason:     req.Reason,
			Note:       req.Note,
		}
		if err := db.Create(&report).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create report"})
			return
		}

		// Auto-collapse topic at threshold
		const threshold = 10
		if req.TargetType == "topic" {
			var count int64
			db.Model(&model.ForumReport{}).Where("target_type = ? AND target_id = ?", "topic", targetUUID).Count(&count)
			if count >= threshold {
				db.Model(&model.ForumTopic{}).Where("id = ?", targetUUID).Update("closed", true)
			}
		}

		c.JSON(http.StatusCreated, gin.H{"message": "reported"})
	}
}

// CreateCategoryRequest submits a request to create a new forum category.
// Route: POST /api/forum/category-requests
// CreateCategoryRequest godoc
// @Summary 提交分类申请
// @Description 当前用户提交一个新的论坛分类创建申请。
// @Tags forum
// @Accept json
// @Produce json
// @Param input body CategoryRequestCreateInput true "分类申请输入"
// @Success 201 {object} CategoryRequestResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/category-requests [post]
func CreateCategoryRequest(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var req struct {
			Name        string `json:"name" binding:"required"`
			Description string `json:"description"`
			Reason      string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		cr := model.CategoryRequest{
			UserID:      userID,
			Name:        req.Name,
			Description: req.Description,
			Reason:      req.Reason,
			Status:      "pending",
		}
		if err := db.Create(&cr).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": cr})
	}
}

// GetCategoryRequests lists pending category requests (admin only).
// Route: GET /api/forum/category-requests
// GetCategoryRequests godoc
// @Summary 获取待审分类申请
// @Description 仅管理员可查看待审的论坛分类申请列表。
// @Tags forum
// @Produce json
// @Success 200 {object} CategoryRequestListResponse
// @Failure 403 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/category-requests [get]
func GetCategoryRequests(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isAdmin(c) {
			c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
			return
		}
		var requests []model.CategoryRequest
		db.Where("status = ?", "pending").Preload("User").Order("created_at ASC").Find(&requests)
		c.JSON(http.StatusOK, gin.H{"data": requests})
	}
}

// ReviewCategoryRequest approves or rejects a category request (admin only).
// Route: POST /api/forum/category-requests/:id/review
// ReviewCategoryRequest godoc
// @Summary 审核分类申请
// @Description 仅管理员可批准或拒绝分类申请；批准时会创建分类。
// @Tags forum
// @Accept json
// @Produce json
// @Param id path string true "分类申请 UUID"
// @Param input body CategoryRequestReviewInput true "审核输入"
// @Success 200 {object} CategoryRequestReviewResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/category-requests/{id}/review [post]
func ReviewCategoryRequest(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isAdmin(c) {
			c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
			return
		}
		id := c.Param("id")
		var req struct {
			Action     string `json:"action" binding:"required,oneof=approve reject"`
			ReviewNote string `json:"review_note"`
			Color      string `json:"color"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		adminID := c.MustGet("userID").(uuid.UUID)
		var cr model.CategoryRequest
		if err := db.First(&cr, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}

		cr.Status = req.Action + "d" // "approved" | "rejected"
		cr.ReviewedBy = &adminID
		cr.ReviewNote = req.ReviewNote
		db.Save(&cr)

		if req.Action == "approve" {
			color := req.Color
			if color == "" {
				color = "#6366f1"
			}
			cat := model.ForumCategory{
				Name:        cr.Name,
				Description: cr.Description,
				Color:       color,
			}
			if err := db.Create(&cat).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "approved but failed to create category"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"data": cr, "category": cat})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": cr})
	}
}
