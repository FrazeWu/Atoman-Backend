package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"atoman/internal/collab"
	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/storage"
)

type dmHandler struct {
	db      *gorm.DB
	userHub *collab.UserHub
	s3      *s3.S3
}

type dmPermissionError struct {
	code    string
	message string
}

func (e *dmPermissionError) Error() string {
	return e.code
}

type dmConversationItem struct {
	ConversationID string     `json:"conversation_id"`
	OtherUsername  string     `json:"other_username"`
	OtherUserID    string     `json:"other_user_id"`
	LastMessageAt  *time.Time `json:"last_message_at"`
	Preview        string     `json:"preview"`
	UnreadCount    int64      `json:"unread_count"`
}

type dmPushPayload struct {
	ConversationID string    `json:"conversation_id"`
	MessageID      string    `json:"message_id"`
	SenderID       string    `json:"sender_id"`
	SenderUsername string    `json:"sender_username"`
	Content        string    `json:"content"`
	ImageURL       string    `json:"image_url"`
	CreatedAt      time.Time `json:"created_at"`
}

func SetupDMRoutes(r *gin.Engine, db *gorm.DB, userHub *collab.UserHub, s3Client *s3.S3) {
	h := &dmHandler{db: db, userHub: userHub, s3: s3Client}

	auth := r.Group("/api/v1/dm")
	auth.Use(middleware.AuthMiddleware())
	{
		auth.GET("/conversations", h.listConversations)
		auth.GET("/conversations/:username", h.getMessages)
		auth.POST("/conversations/:username", h.sendMessage)
		auth.PUT("/conversations/:username/read", h.markRead)
		auth.GET("/unread-count", h.unreadCount)
		auth.POST("/upload", h.uploadImage)
	}
}

// listConversations godoc
// @Summary 获取私信会话列表
// @Description 返回当前用户的私信会话列表和未读计数。
// @Tags dm
// @Produce json
// @Success 200 {object} DMConversationListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/dm/conversations [get]
func (h *dmHandler) listConversations(c *gin.Context) {
	userID := mustGetUserUUID(c)
	if userID == uuid.Nil {
		return
	}

	var conversations []model.DMConversation
	if err := h.db.Where("participant_a = ? OR participant_b = ?", userID, userID).
		Order("last_message_at DESC NULLS LAST").
		Find(&conversations).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch conversations"})
		return
	}

	result := make([]dmConversationItem, 0, len(conversations))
	for _, conversation := range conversations {
		otherID := conversation.ParticipantA
		if otherID == userID {
			otherID = conversation.ParticipantB
		}
		var other model.User
		if err := h.db.Select("uuid", "username").First(&other, "uuid = ?", otherID).Error; err != nil {
			continue
		}
		var unread int64
		if err := h.db.Model(&model.DMMessage{}).
			Where("conversation_id = ? AND sender_id != ? AND read_at IS NULL", conversation.ID, userID).
			Count(&unread).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count unread messages"})
			return
		}
		result = append(result, dmConversationItem{
			ConversationID: conversation.ID.String(),
			OtherUsername:  other.Username,
			OtherUserID:    other.UUID.String(),
			LastMessageAt:  conversation.LastMessageAt,
			Preview:        conversation.LastMessagePreview,
			UnreadCount:    unread,
		})
	}

	c.JSON(http.StatusOK, gin.H{"data": result})
}

// getMessages godoc
// @Summary 获取会话消息列表
// @Description 按对方用户名获取私信会话消息，支持分页并返回固定 page_size。
// @Tags dm
// @Produce json
// @Param username path string true "对方用户名"
// @Param page query int false "页码" default(1)
// @Success 200 {object} DMMessageListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/dm/conversations/{username} [get]
func (h *dmHandler) getMessages(c *gin.Context) {
	userID := mustGetUserUUID(c)
	if userID == uuid.Nil {
		return
	}

	other, ok := h.findUserByUsername(c, c.Param("username"))
	if !ok {
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const pageSize = 30

	conversation, err := h.findConversation(userID, other.UUID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusOK, gin.H{"data": []model.DMMessage{}, "total": 0, "page": page, "page_size": pageSize})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get conversation"})
		return
	}

	var messages []model.DMMessage
	var total int64
	query := h.db.Model(&model.DMMessage{}).Where("conversation_id = ?", conversation.ID)
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count messages"})
		return
	}
	if err := query.Preload("Sender").Order("dm_messages.created_at ASC, dm_messages.id ASC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&messages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch messages"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": messages, "total": total, "page": page, "page_size": pageSize})
}

// sendMessage godoc
// @Summary 发送私信
// @Description 向指定用户名发送文本或图片私信。
// @Tags dm
// @Accept json
// @Produce json
// @Param username path string true "对方用户名"
// @Param input body DMSendInput true "私信输入"
// @Success 201 {object} DMMessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/dm/conversations/{username} [post]
func (h *dmHandler) sendMessage(c *gin.Context) {
	senderID := mustGetUserUUID(c)
	if senderID == uuid.Nil {
		return
	}

	other, ok := h.findUserByUsername(c, c.Param("username"))
	if !ok {
		return
	}
	if other.UUID == senderID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot_message_self", "message": "不能给自己发私信"})
		return
	}

	var input struct {
		Content  string `json:"content"`
		ImageURL string `json:"image_url"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	input.ImageURL = strings.TrimSpace(input.ImageURL)
	if input.Content == "" && input.ImageURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty_message", "message": "内容和图片不能同时为空"})
		return
	}

	preview := input.Content
	if preview == "" {
		preview = "[图片]"
	}

	var message model.DMMessage
	var conversationID uuid.UUID
	err := h.db.Transaction(func(tx *gorm.DB) error {
		conversation, err := h.sendConversationForRecipient(tx, senderID, other.UUID)
		if err != nil {
			return err
		}
		conversationID = conversation.ID

		message = model.DMMessage{
			ConversationID: conversation.ID,
			SenderID:       senderID,
			Content:        input.Content,
			ImageURL:       input.ImageURL,
		}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}

		return tx.Model(&model.DMConversation{}).
			Where("id = ? AND (last_message_at IS NULL OR last_message_at <= ?)", conversation.ID, message.CreatedAt).
			Updates(map[string]interface{}{
				"last_message_at":      message.CreatedAt,
				"last_message_preview": truncateDMPreview(preview, 100),
			}).Error
	})
	if err != nil {
		var permissionErr *dmPermissionError
		if errors.As(err, &permissionErr) {
			c.JSON(http.StatusForbidden, gin.H{"error": permissionErr.code, "message": permissionErr.message})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send message"})
		return
	}

	var sender model.User
	h.db.Select("uuid", "username").First(&sender, "uuid = ?", senderID)
	payload := dmPushPayload{
		ConversationID: conversationID.String(),
		MessageID:      message.ID.String(),
		SenderID:       senderID.String(),
		SenderUsername: sender.Username,
		Content:        message.Content,
		ImageURL:       message.ImageURL,
		CreatedAt:      message.CreatedAt,
	}
	if h.userHub != nil {
		h.userHub.Push(other.UUID, "dm", payload)
		h.userHub.Push(senderID, "dm", payload)
	}

	c.JSON(http.StatusCreated, gin.H{"data": message})
}

// markRead godoc
// @Summary 标记会话已读
// @Description 将与指定用户名的会话中对方发来的未读消息全部标记为已读。
// @Tags dm
// @Produce json
// @Param username path string true "对方用户名"
// @Success 200 {object} BoolStatusResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/dm/conversations/{username}/read [put]
func (h *dmHandler) markRead(c *gin.Context) {
	userID := mustGetUserUUID(c)
	if userID == uuid.Nil {
		return
	}

	other, ok := h.findUserByUsername(c, c.Param("username"))
	if !ok {
		return
	}

	conversation, err := h.findConversation(userID, other.UUID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark read"})
		return
	}

	now := time.Now()
	if err := h.db.Model(&model.DMMessage{}).
		Where("conversation_id = ? AND sender_id != ? AND read_at IS NULL", conversation.ID, userID).
		Update("read_at", now).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// unreadCount godoc
// @Summary 获取未读私信数
// @Description 返回当前用户的未读私信数量。
// @Tags dm
// @Produce json
// @Success 200 {object} CountResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/dm/unread-count [get]
func (h *dmHandler) unreadCount(c *gin.Context) {
	userID := mustGetUserUUID(c)
	if userID == uuid.Nil {
		return
	}

	var count int64
	if err := h.db.Model(&model.DMMessage{}).
		Joins("JOIN dm_conversations ON dm_conversations.id = dm_messages.conversation_id").
		Where("dm_messages.sender_id != ? AND dm_messages.read_at IS NULL", userID).
		Where("dm_conversations.participant_a = ? OR dm_conversations.participant_b = ?", userID, userID).
		Count(&count).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count unread messages"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"count": count})
}

// uploadImage godoc
// @Summary 上传私信图片
// @Description 上传一张私信图片，支持 JPEG、PNG、GIF、WebP，最大 10MB。
// @Tags dm
// @Accept mpfd
// @Produce json
// @Param image formData file true "图片文件"
// @Success 200 {object} ImageUploadResponse
// @Failure 400 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/dm/upload [post]
func (h *dmHandler) uploadImage(c *gin.Context) {
	userID := mustGetUserUUID(c)
	if userID == uuid.Nil {
		return
	}

	file, header, err := c.Request.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Image file is required (field name: image)"})
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	allowed := map[string]bool{
		"image/jpeg": true,
		"image/png":  true,
		"image/gif":  true,
		"image/webp": true,
	}
	if !allowed[contentType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only JPEG, PNG, GIF, and WebP images are allowed"})
		return
	}
	if !uploadContentMatchesDeclared(file, contentType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Image content does not match declared type"})
		return
	}

	const maxSize = 10 * 1024 * 1024
	if header.Size > maxSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Image must be smaller than 10 MB"})
		return
	}

	ext := contentTypeToExt(contentType)
	filename := uuid.New().String() + ext
	pathUserID := userID.String()
	s3Key := "dm/images/" + pathUserID + "/" + filename

	if os.Getenv("STORAGE_TYPE") == "local" {
		localDir := filepath.Join("uploads", "dm", "images", pathUserID)
		if err := os.MkdirAll(localDir, 0o755); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create upload directory"})
			return
		}
		destPath := filepath.Join(localDir, filename)
		if err := storage.SaveFileToPath(file, destPath); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save image"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"image_url": "/uploads/dm/images/" + pathUserID + "/" + filename})
		return
	}

	if !requireS3(c, h.s3) {
		return
	}
	if _, err := h.s3.PutObject(&s3.PutObjectInput{
		Bucket:      aws.String(os.Getenv("S3_BUCKET")),
		Key:         aws.String(s3Key),
		Body:        file,
		ContentType: aws.String(contentType),
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload image to storage"})
		return
	}

	imageURL := strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/") + "/" + s3Key
	c.JSON(http.StatusOK, gin.H{"image_url": imageURL})
}

func normalizeUsername(raw string) string {
	return strings.TrimSpace(raw)
}

func (h *dmHandler) findUserByUsername(c *gin.Context, username string) (*model.User, bool) {
	normalized := normalizeUsername(username)
	if normalized == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_username", "message": "用户名不能为空"})
		return nil, false
	}

	var user model.User
	if err := h.db.Where("LOWER(username) = LOWER(?)", normalized).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user_not_found", "message": "用户不存在"})
		return nil, false
	}
	return &user, true
}

func (h *dmHandler) getOrCreateConversation(userA, userB uuid.UUID) (*model.DMConversation, error) {
	participantA, participantB := normalizeConversationParticipants(userA, userB)
	var conversation model.DMConversation
	err := h.db.Where("participant_a = ? AND participant_b = ?", participantA, participantB).First(&conversation).Error
	if err == nil {
		return &conversation, nil
	}
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	conversation = model.DMConversation{ParticipantA: participantA, ParticipantB: participantB}
	if err := h.db.Create(&conversation).Error; err != nil {
		if err2 := h.db.Where("participant_a = ? AND participant_b = ?", participantA, participantB).First(&conversation).Error; err2 == nil {
			return &conversation, nil
		}
		return nil, err
	}
	return &conversation, nil
}

func (h *dmHandler) findConversation(userA, userB uuid.UUID) (*model.DMConversation, error) {
	participantA, participantB := normalizeConversationParticipants(userA, userB)
	var conversation model.DMConversation
	err := h.db.Where("participant_a = ? AND participant_b = ?", participantA, participantB).First(&conversation).Error
	if err != nil {
		return nil, err
	}
	return &conversation, nil
}

func (h *dmHandler) sendConversationForRecipient(tx *gorm.DB, senderID, recipientID uuid.UUID) (*model.DMConversation, error) {
	var settings model.UserSettings
	if err := tx.Where("user_id = ?", recipientID).First(&settings).Error; err != nil {
		settings = model.UserSettings{UserID: recipientID, DMPermission: "anyone"}
	}
	permission := settings.DMPermission
	if permission == "" {
		permission = "anyone"
	}

	switch permission {
	case "anyone":
		return h.getOrCreateConversationTx(tx, senderID, recipientID, true)
	case "following_only":
		var count int64
		tx.Model(&model.Follow{}).
			Where("follower_id = ? AND following_id = ?", recipientID, senderID).
			Count(&count)
		if count == 0 {
			return nil, &dmPermissionError{code: "dm_permission_denied", message: "仅对方关注的用户可发送私信"}
		}
		return h.getOrCreateConversationTx(tx, senderID, recipientID, true)
	case "one_before_reply":
		conversation, err := h.getOrCreateConversationTx(tx, senderID, recipientID, true)
		if err != nil {
			return nil, err
		}

		var senderCount int64
		tx.Model(&model.DMMessage{}).Where("conversation_id = ? AND sender_id = ?", conversation.ID, senderID).Count(&senderCount)
		if senderCount == 0 {
			return conversation, nil
		}
		var recipientReplyCount int64
		tx.Model(&model.DMMessage{}).Where("conversation_id = ? AND sender_id = ?", conversation.ID, recipientID).Count(&recipientReplyCount)
		if recipientReplyCount == 0 {
			return nil, &dmPermissionError{code: "dm_waiting_reply", message: "对方设置了回复前仅可发送一条消息"}
		}
		return conversation, nil
	default:
		return h.getOrCreateConversationTx(tx, senderID, recipientID, true)
	}
}

func (h *dmHandler) getOrCreateConversationTx(tx *gorm.DB, userA, userB uuid.UUID, forUpdate bool) (*model.DMConversation, error) {
	participantA, participantB := normalizeConversationParticipants(userA, userB)
	query := tx.Model(&model.DMConversation{})
	if forUpdate && tx.Dialector.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}

	var conversation model.DMConversation
	err := query.Where("participant_a = ? AND participant_b = ?", participantA, participantB).First(&conversation).Error
	if err == nil {
		return &conversation, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	conversation = model.DMConversation{ParticipantA: participantA, ParticipantB: participantB}
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "participant_a"}, {Name: "participant_b"}},
		DoNothing: true,
	}).Create(&conversation)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected > 0 {
		return &conversation, nil
	}

	query = tx.Model(&model.DMConversation{})
	if forUpdate && tx.Dialector.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Where("participant_a = ? AND participant_b = ?", participantA, participantB).First(&conversation).Error; err != nil {
		return nil, err
	}
	return &conversation, nil
}

func mustGetUserUUID(c *gin.Context) uuid.UUID {
	value, ok := c.Get("user_id")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return uuid.Nil
	}
	userID, ok := value.(uuid.UUID)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid user id"})
		return uuid.Nil
	}
	return userID
}

func normalizeConversationParticipants(a, b uuid.UUID) (uuid.UUID, uuid.UUID) {
	if strings.Compare(a.String(), b.String()) <= 0 {
		return a, b
	}
	return b, a
}

func truncateDMPreview(s string, maxLen int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= maxLen {
		return string(runes)
	}
	return string(runes[:maxLen]) + "…"
}

var _ = fmt.Sprintf
