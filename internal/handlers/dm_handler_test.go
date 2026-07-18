package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newDMTestRouter(t *testing.T) (*gin.Engine, *gorm.DB, model.User, model.User) {
	return newDMTestRouterWithS3(t, nil)
}

func newDMTestRouterWithS3(t *testing.T, s3Client *s3.S3) (*gin.Engine, *gorm.DB, model.User, model.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")

	dbPath := filepath.Join(t.TempDir(), "dm-test.sqlite")
	dsn := fmt.Sprintf("file:%s?_txlock=immediate&_pragma=busy_timeout(5000)", dbPath)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.UserSettings{},
		&model.Follow{},
		&model.UserBlock{},
		&model.DMConversation{},
		&model.DMMessage{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_dm_conversation
		ON dm_conversations (participant_a, participant_b)`).Error; err != nil {
		t.Fatalf("create dm conversation index: %v", err)
	}
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_dm_message_conv_sender_read
		ON dm_messages (conversation_id, sender_id, read_at)`).Error; err != nil {
		t.Fatalf("create dm message index: %v", err)
	}
	middleware.SetAuthDB(db)

	sender := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&sender).Error; err != nil {
		t.Fatalf("create sender: %v", err)
	}

	recipient := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&recipient).Error; err != nil {
		t.Fatalf("create recipient: %v", err)
	}

	r := gin.New()
	SetupDMRoutes(r, db, nil, s3Client)
	return r, db, sender, recipient
}

func TestSendMessageIsRejectedWhenEitherUserBlockedTheOther(t *testing.T) {
	r, db, sender, recipient := newDMTestRouter(t)

	if err := db.Create(&model.UserBlock{BlockerID: recipient.UUID, BlockedID: sender.UUID}).Error; err != nil {
		t.Fatalf("create block: %v", err)
	}
	body := bytes.NewBufferString(`{"content":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dm/conversations/"+recipient.Username, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", dmAuthHeader(t, sender))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected blocked send 403, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "dm_blocked") {
		t.Fatalf("expected dm_blocked error, got %s", w.Body.String())
	}
	var messages int64
	if err := db.Model(&model.DMMessage{}).Count(&messages).Error; err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if messages != 0 {
		t.Fatalf("expected no messages after blocked send, got %d", messages)
	}
}

func TestListConversationsReportsBidirectionalBlockedState(t *testing.T) {
	r, db, sender, recipient := newDMTestRouter(t)
	conversation := model.DMConversation{ParticipantA: sender.UUID, ParticipantB: recipient.UUID}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := db.Create(&model.UserBlock{BlockerID: recipient.UUID, BlockedID: sender.UUID}).Error; err != nil {
		t.Fatalf("create block: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dm/conversations", nil)
	req.Header.Set("Authorization", dmAuthHeader(t, sender))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []struct {
			OtherUserID string `json:"other_user_id"`
			IsBlocked   bool   `json:"is_blocked"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode conversations: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].OtherUserID != recipient.UUID.String() || !response.Data[0].IsBlocked {
		t.Fatalf("expected blocked conversation, got %s", w.Body.String())
	}
}

func dmAuthHeader(t *testing.T, user model.User) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return "Bearer " + signed
}

func multipartDMImageBody(t *testing.T, filename, contentType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="image"; filename="` + filename + `"`},
		"Content-Type":        {contentType},
	})
	if err != nil {
		t.Fatalf("create image part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write image part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, writer.FormDataContentType()
}

func TestListConversationsReturnsServerErrorWhenUnreadCountFails(t *testing.T) {
	r, db, sender, recipient := newDMTestRouter(t)

	participantA, participantB := normalizeConversationParticipants(sender.UUID, recipient.UUID)
	if err := db.Create(&model.DMConversation{ParticipantA: participantA, ParticipantB: participantB}).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	callbackName := "test:fail_dm_unread_count"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*int64); ok && tx.Statement.Table == "dm_messages" {
			tx.AddError(errors.New("forced unread count failure"))
		}
	}); err != nil {
		t.Fatalf("register unread count failure callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dm/conversations", nil)
	req.Header.Set("Authorization", dmAuthHeader(t, sender))
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when unread count fails, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestGetMessagesWithoutExistingConversationDoesNotCreateEmptyConversation(t *testing.T) {
	r, db, sender, recipient := newDMTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dm/conversations/"+recipient.Username, nil)
	req.Header.Set("Authorization", dmAuthHeader(t, sender))
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected empty message list to return 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Data     []model.DMMessage `json:"data"`
		Total    int64             `json:"total"`
		Page     int               `json:"page"`
		PageSize int               `json:"page_size"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) != 0 || payload.Total != 0 || payload.Page != 1 || payload.PageSize != 30 {
		t.Fatalf("expected empty first page, got %+v", payload)
	}

	var count int64
	if err := db.Model(&model.DMConversation{}).Count(&count).Error; err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if count != 0 {
		t.Fatalf("GET messages must not create an empty conversation, got %d conversation(s)", count)
	}
}

func TestGetMessagesTreatsSoftDeletedConversationAsMissingWithoutWriting(t *testing.T) {
	r, db, sender, recipient := newDMTestRouter(t)
	participantA, participantB := normalizeConversationParticipants(sender.UUID, recipient.UUID)
	conversation := model.DMConversation{ParticipantA: participantA, ParticipantB: participantB}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := db.Delete(&conversation).Error; err != nil {
		t.Fatalf("soft delete conversation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dm/conversations/"+recipient.Username, nil)
	req.Header.Set("Authorization", dmAuthHeader(t, sender))
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected soft-deleted conversation to return 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Data     []model.DMMessage `json:"data"`
		Total    int64             `json:"total"`
		Page     int               `json:"page"`
		PageSize int               `json:"page_size"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) != 0 || payload.Total != 0 || payload.Page != 1 || payload.PageSize != 30 {
		t.Fatalf("expected empty first page, got %+v", payload)
	}

	var stored []model.DMConversation
	if err := db.Unscoped().Find(&stored).Error; err != nil {
		t.Fatalf("load conversations including soft-deleted: %v", err)
	}
	if len(stored) != 1 || stored[0].ID != conversation.ID || !stored[0].DeletedAt.Valid {
		t.Fatalf("GET must preserve the one soft-deleted conversation, got %+v", stored)
	}
}

func TestGetMessagesReadsNormalizedConversationInBothDirectionsWithPagination(t *testing.T) {
	r, db, sender, recipient := newDMTestRouter(t)
	participantA, participantB := normalizeConversationParticipants(sender.UUID, recipient.UUID)
	conversation := model.DMConversation{ParticipantA: participantA, ParticipantB: participantB}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	createdAt := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	for i := 30; i >= 0; i-- {
		senderID := sender.UUID
		if i%2 == 1 {
			senderID = recipient.UUID
		}
		message := model.DMMessage{
			ConversationID: conversation.ID,
			SenderID:       senderID,
			Content:        fmt.Sprintf("message-%02d", i),
		}
		message.ID = uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012x", i+1))
		message.CreatedAt = createdAt
		if err := db.Create(&message).Error; err != nil {
			t.Fatalf("create message %d: %v", i, err)
		}
	}

	assertPage := func(current model.User, otherUsername string, page int, wantContents ...string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/dm/conversations/%s?page=%d", otherUsername, page), nil)
		req.Header.Set("Authorization", dmAuthHeader(t, current))
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected page %d to return 200, got %d: %s", page, resp.Code, resp.Body.String())
		}
		var payload struct {
			Data     []model.DMMessage `json:"data"`
			Total    int64             `json:"total"`
			Page     int               `json:"page"`
			PageSize int               `json:"page_size"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode page %d: %v", page, err)
		}
		if payload.Total != 31 || payload.Page != page || payload.PageSize != 30 || len(payload.Data) != len(wantContents) {
			t.Fatalf("unexpected page metadata: %+v", payload)
		}
		for i, want := range wantContents {
			if payload.Data[i].Content != want {
				t.Fatalf("page %d item %d: expected %q, got %q", page, i, want, payload.Data[i].Content)
			}
		}
	}

	pageOneContents := make([]string, 30)
	for i := range pageOneContents {
		pageOneContents[i] = fmt.Sprintf("message-%02d", i)
	}
	assertPage(sender, recipient.Username, 1, pageOneContents...)
	assertPage(recipient, sender.Username, 2, "message-30")
}

func TestGetMessagesReturnsServerErrorWhenCountFails(t *testing.T) {
	r, db, sender, recipient := newDMTestRouter(t)
	participantA, participantB := normalizeConversationParticipants(sender.UUID, recipient.UUID)
	conversation := model.DMConversation{ParticipantA: participantA, ParticipantB: participantB}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := db.Create(&model.DMMessage{ConversationID: conversation.ID, SenderID: sender.UUID, Content: "message"}).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	callbackName := "test:fail_dm_message_count"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*int64); ok && tx.Statement.Table == "dm_messages" {
			tx.AddError(errors.New("forced message count failure"))
		}
	}); err != nil {
		t.Fatalf("register message count failure callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dm/conversations/"+recipient.Username, nil)
	req.Header.Set("Authorization", dmAuthHeader(t, sender))
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected count failure to return 500, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if payload["error"] != "failed to count messages" {
		t.Fatalf("expected count error to be handled before fetching messages, got %s", resp.Body.String())
	}
}

func TestGetMessagesCannotReadConversationBetweenOtherUsers(t *testing.T) {
	r, db, sender, recipient := newDMTestRouter(t)
	participantA, participantB := normalizeConversationParticipants(sender.UUID, recipient.UUID)
	conversation := model.DMConversation{ParticipantA: participantA, ParticipantB: participantB}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := db.Create(&model.DMMessage{ConversationID: conversation.ID, SenderID: sender.UUID, Content: "private"}).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	third := model.User{Username: "charlie", Email: "charlie@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&third).Error; err != nil {
		t.Fatalf("create third user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dm/conversations/"+sender.Username, nil)
	req.Header.Set("Authorization", dmAuthHeader(t, third))
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected unrelated conversation lookup to return empty 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Data     []model.DMMessage `json:"data"`
		Total    int64             `json:"total"`
		Page     int               `json:"page"`
		PageSize int               `json:"page_size"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) != 0 || payload.Total != 0 || payload.Page != 1 || payload.PageSize != 30 {
		t.Fatalf("expected no access to other users' messages, got %+v", payload)
	}

	var count int64
	if err := db.Model(&model.DMConversation{}).Count(&count).Error; err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if count != 1 {
		t.Fatalf("GET must not create a third-party conversation, got %d records", count)
	}
}

func TestDMUploadImageRejectsSpoofedImageContentType(t *testing.T) {
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")

	var s3Path string
	var s3ContentType string
	r, _, sender, _ := newDMTestRouterWithS3(t, fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType))

	body, contentType := multipartDMImageBody(t, "message.png", "image/png", []byte("not really a png"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dm/upload", body)
	req.Header.Set("Authorization", dmAuthHeader(t, sender))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for spoofed image content, got %d: %s", w.Code, w.Body.String())
	}
	if s3Path != "" || s3ContentType != "" {
		t.Fatalf("expected spoofed upload to be rejected before S3, got path=%q contentType=%q", s3Path, s3ContentType)
	}
}

func TestDMUploadImageAcceptsValidPNGContent(t *testing.T) {
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")

	var s3Path string
	var s3ContentType string
	var s3ACL string
	r, _, sender, _ := newDMTestRouterWithS3(t, fakeS3ClientForUploadTestWithACL(t, &s3Path, &s3ContentType, &s3ACL))

	body, contentType := multipartDMImageBody(t, "message.png", "image/png", validPNGBytes())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dm/upload", body)
	req.Header.Set("Authorization", dmAuthHeader(t, sender))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid png content, got %d: %s", w.Code, w.Body.String())
	}
	if s3ContentType != "image/png" {
		t.Fatalf("expected S3 content type image/png, got %q", s3ContentType)
	}
	if !strings.HasPrefix(s3Path, "/atoman-test/dm/images/"+sender.UUID.String()+"/") {
		t.Fatalf("expected dm image S3 path for sender, got %q", s3Path)
	}
	if s3ACL != "" {
		t.Fatalf("expected dm image upload not to set S3 ACL, got %q", s3ACL)
	}
}

func TestSendMessageOneBeforeReplyBlocksSecondSendWithoutReply(t *testing.T) {
	r, db, sender, recipient := newDMTestRouter(t)

	settings := model.UserSettings{UserID: recipient.UUID, DMPermission: "one_before_reply"}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatalf("create settings: %v", err)
	}

	body, err := json.Marshal(map[string]any{"content": "hello"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	firstReq := httptest.NewRequest(http.MethodPost, "/api/v1/dm/conversations/"+recipient.Username, bytes.NewReader(body))
	firstReq.Header.Set("Content-Type", "application/json")
	firstReq.Header.Set("Authorization", dmAuthHeader(t, sender))
	firstResp := httptest.NewRecorder()
	r.ServeHTTP(firstResp, firstReq)
	if firstResp.Code != http.StatusCreated {
		t.Fatalf("expected first send to succeed, got %d: %s", firstResp.Code, firstResp.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1/dm/conversations/"+recipient.Username, bytes.NewReader(body))
	secondReq.Header.Set("Content-Type", "application/json")
	secondReq.Header.Set("Authorization", dmAuthHeader(t, sender))
	secondResp := httptest.NewRecorder()
	r.ServeHTTP(secondResp, secondReq)
	if secondResp.Code != http.StatusForbidden {
		t.Fatalf("expected second send to be blocked, got %d: %s", secondResp.Code, secondResp.Body.String())
	}

	var count int64
	if err := db.Model(&model.DMMessage{}).Count(&count).Error; err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 dm message, got %d", count)
	}
}

func TestSendMessageDoesNotOverwriteNewerConversationSummary(t *testing.T) {
	r, db, sender, recipient := newDMTestRouter(t)

	participantA, participantB := normalizeConversationParticipants(sender.UUID, recipient.UUID)
	newerAt := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	conversation := model.DMConversation{
		ParticipantA:       participantA,
		ParticipantB:       participantB,
		LastMessageAt:      &newerAt,
		LastMessagePreview: "newer message",
	}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	body, err := json.Marshal(map[string]any{"content": "older message"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dm/conversations/"+recipient.Username, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", dmAuthHeader(t, sender))
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected send to succeed, got %d: %s", resp.Code, resp.Body.String())
	}

	var refreshed model.DMConversation
	if err := db.First(&refreshed, "id = ?", conversation.ID).Error; err != nil {
		t.Fatalf("fetch conversation: %v", err)
	}
	if refreshed.LastMessagePreview != "newer message" {
		t.Fatalf("expected newer preview to remain, got %q", refreshed.LastMessagePreview)
	}
	if refreshed.LastMessageAt == nil || !refreshed.LastMessageAt.Equal(newerAt) {
		t.Fatalf("expected newer last_message_at to remain, got %v", refreshed.LastMessageAt)
	}
}

func TestSendMessageOneBeforeReplyConcurrentSendsPersistAtMostOne(t *testing.T) {
	r, db, sender, recipient := newDMTestRouter(t)

	settings := model.UserSettings{UserID: recipient.UUID, DMPermission: "one_before_reply"}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatalf("create settings: %v", err)
	}

	participantA, participantB := normalizeConversationParticipants(sender.UUID, recipient.UUID)
	conversation := model.DMConversation{ParticipantA: participantA, ParticipantB: participantB}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	body, err := json.Marshal(map[string]any{"content": "hello"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			req := httptest.NewRequest(http.MethodPost, "/api/v1/dm/conversations/"+recipient.Username, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", dmAuthHeader(t, sender))
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)
			statuses <- resp.Code
		}()
	}

	close(start)
	wg.Wait()
	close(statuses)

	var created int
	for status := range statuses {
		if status == http.StatusCreated {
			created++
			continue
		}
		if status != http.StatusForbidden {
			t.Fatalf("expected concurrent send to return 201 or 403, got %d", status)
		}
	}
	if created > 1 {
		t.Fatalf("expected at most one concurrent send to succeed, got %d", created)
	}

	var count int64
	if err := db.Model(&model.DMMessage{}).Where("conversation_id = ?", conversation.ID).Count(&count).Error; err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != int64(created) || count > 1 {
		t.Fatalf("expected persisted messages to match successes and be at most 1, successes=%d count=%d", created, count)
	}
}
