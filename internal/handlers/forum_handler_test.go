package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newForumHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumReply{},
		&model.ForumLike{},
		&model.ForumBookmark{},
		&model.CategoryRequest{},
	)
	return db
}

func seedForumLikeTestUser(t *testing.T, db *gorm.DB, username string) model.User {
	t.Helper()

	user := model.User{
		Username: username,
		Email:    username + "@example.com",
		Password: "hash",
		Role:     "user",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func seedForumLikeTestTopic(t *testing.T, db *gorm.DB, user model.User) model.ForumTopic {
	t.Helper()

	category := model.ForumCategory{Name: "General", Description: "General discussion", Color: "#000000"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}

	topic := model.ForumTopic{
		UserID:     user.UUID,
		CategoryID: category.ID,
		Title:      "Hello",
		Content:    "World",
	}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	return topic
}

func seedForumLikeTestReply(t *testing.T, db *gorm.DB, user model.User) (model.ForumTopic, model.ForumReply) {
	t.Helper()

	topic := seedForumLikeTestTopic(t, db, user)
	reply := model.ForumReply{
		TopicID: topic.ID,
		UserID:  user.UUID,
		Content: "Reply body",
	}
	if err := db.Create(&reply).Error; err != nil {
		t.Fatalf("create reply: %v", err)
	}
	return topic, reply
}

func withForumLikeAuth(userID uuid.UUID, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("user_id", userID)
		h(c)
	}
}

func installForumLikeCreateConflict(t *testing.T, db *gorm.DB, like model.ForumLike) {
	t.Helper()

	callbackName := "forum_like_create_conflict_" + strings.ReplaceAll(t.Name(), "/", "_")
	seeded := false
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if seeded || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "ForumLike" {
			return
		}
		seeded = true
		competing := like
		if err := tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Create(&competing).Error; err != nil {
			t.Fatalf("seed competing like: %v", err)
		}
	}); err != nil {
		t.Fatalf("register create conflict callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Create().Remove(callbackName)
	})
}

func installForumLikeCreateConflictWithCount(t *testing.T, db *gorm.DB, like model.ForumLike) {
	t.Helper()

	callbackName := "forum_like_create_conflict_count_" + strings.ReplaceAll(t.Name(), "/", "_")
	seeded := false
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if seeded || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "ForumLike" {
			return
		}
		seeded = true
		competing := like
		if err := tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Create(&competing).Error; err != nil {
			t.Fatalf("seed competing like: %v", err)
		}
		incrementForumLikeCount(t, tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}), like.TargetType, like.TargetID)
	}); err != nil {
		t.Fatalf("register create conflict callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Create().Remove(callbackName)
	})
}

func installForumLikeDeleteConflictWithCount(t *testing.T, db *gorm.DB, like model.ForumLike) {
	t.Helper()

	callbackName := "forum_like_delete_conflict_count_" + strings.ReplaceAll(t.Name(), "/", "_")
	deleted := false
	if err := db.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		if deleted || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "ForumLike" {
			return
		}
		deleted = true
		if err := tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Where(
			"user_id = ? AND target_type = ? AND target_id = ?",
			like.UserID,
			like.TargetType,
			like.TargetID,
		).Delete(&model.ForumLike{}).Error; err != nil {
			t.Fatalf("delete competing like: %v", err)
		}
		decrementForumLikeCount(t, tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}), like.TargetType, like.TargetID)
	}); err != nil {
		t.Fatalf("register delete conflict callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Delete().Remove(callbackName)
	})
}

func incrementForumLikeCount(t *testing.T, db *gorm.DB, targetType string, targetID uuid.UUID) {
	t.Helper()

	switch targetType {
	case "topic":
		if err := db.Model(&model.ForumTopic{}).Where("id = ?", targetID).UpdateColumn("like_count", gorm.Expr("like_count + 1")).Error; err != nil {
			t.Fatalf("increment topic like_count: %v", err)
		}
	case "reply":
		if err := db.Model(&model.ForumReply{}).Where("id = ?", targetID).UpdateColumn("like_count", gorm.Expr("like_count + 1")).Error; err != nil {
			t.Fatalf("increment reply like_count: %v", err)
		}
	default:
		t.Fatalf("unknown target type %q", targetType)
	}
}

func decrementForumLikeCount(t *testing.T, db *gorm.DB, targetType string, targetID uuid.UUID) {
	t.Helper()

	switch targetType {
	case "topic":
		if err := db.Model(&model.ForumTopic{}).Where("id = ?", targetID).UpdateColumn("like_count", gorm.Expr("like_count - 1")).Error; err != nil {
			t.Fatalf("decrement topic like_count: %v", err)
		}
	case "reply":
		if err := db.Model(&model.ForumReply{}).Where("id = ?", targetID).UpdateColumn("like_count", gorm.Expr("like_count - 1")).Error; err != nil {
			t.Fatalf("decrement reply like_count: %v", err)
		}
	default:
		t.Fatalf("unknown target type %q", targetType)
	}
}

func assertForumLikeRows(t *testing.T, db *gorm.DB, targetType string, targetID uuid.UUID, want int64) {
	t.Helper()

	var got int64
	if err := db.Model(&model.ForumLike{}).Where("target_type = ? AND target_id = ?", targetType, targetID).Count(&got).Error; err != nil {
		t.Fatalf("count likes: %v", err)
	}
	if got != want {
		t.Fatalf("expected %d like rows, got %d", want, got)
	}
}

func assertForumTopicLikeCount(t *testing.T, db *gorm.DB, topicID uuid.UUID, want int) {
	t.Helper()

	var topic model.ForumTopic
	if err := db.First(&topic, "id = ?", topicID).Error; err != nil {
		t.Fatalf("load topic: %v", err)
	}
	if topic.LikeCount != want {
		t.Fatalf("expected topic like_count %d, got %d", want, topic.LikeCount)
	}
}

func assertForumReplyLikeCount(t *testing.T, db *gorm.DB, replyID uuid.UUID, want int) {
	t.Helper()

	var reply model.ForumReply
	if err := db.First(&reply, "id = ?", replyID).Error; err != nil {
		t.Fatalf("load reply: %v", err)
	}
	if reply.LikeCount != want {
		t.Fatalf("expected reply like_count %d, got %d", want, reply.LikeCount)
	}
}

func TestToggleForumTopicLikeKeepsCountStableAcrossCreateConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newForumHandlerTestDB(t)
	user := seedForumLikeTestUser(t, db, "topic-author")
	topic := seedForumLikeTestTopic(t, db, user)
	installForumLikeCreateConflict(t, db, model.ForumLike{UserID: user.UUID, TargetType: "topic", TargetID: topic.ID})

	r := gin.New()
	r.POST("/topics/:id/like", withForumLikeAuth(user.UUID, (&forumHandler{db: db}).ToggleForumTopicLike()))

	req := httptest.NewRequest(http.MethodPost, "/topics/"+topic.ID.String()+"/like", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"liked":true`) {
		t.Fatalf("expected liked=true response, got %s", w.Body.String())
	}

	var gotTopic model.ForumTopic
	if err := db.First(&gotTopic, "id = ?", topic.ID).Error; err != nil {
		t.Fatalf("load topic: %v", err)
	}
	if gotTopic.LikeCount != 0 {
		t.Fatalf("expected like_count to stay 0, got %d", gotTopic.LikeCount)
	}
	var likeCount int64
	if err := db.Model(&model.ForumLike{}).Where("target_type = ? AND target_id = ?", "topic", topic.ID).Count(&likeCount).Error; err != nil {
		t.Fatalf("count likes: %v", err)
	}
	if likeCount != 1 {
		t.Fatalf("expected exactly 1 like row, got %d", likeCount)
	}
}

func TestToggleForumTopicLikeDoesNotDriftOnDuplicateLike(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newForumHandlerTestDB(t)
	user := seedForumLikeTestUser(t, db, "topic-duplicate-like")
	topic := seedForumLikeTestTopic(t, db, user)
	installForumLikeCreateConflictWithCount(t, db, model.ForumLike{UserID: user.UUID, TargetType: "topic", TargetID: topic.ID})

	r := gin.New()
	r.POST("/topics/:id/like", withForumLikeAuth(user.UUID, (&forumHandler{db: db}).ToggleForumTopicLike()))

	req := httptest.NewRequest(http.MethodPost, "/topics/"+topic.ID.String()+"/like", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	assertForumLikeRows(t, db, "topic", topic.ID, 1)
	assertForumTopicLikeCount(t, db, topic.ID, 1)
}

func TestToggleForumTopicLikeDoesNotDriftOnDuplicateUnlike(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newForumHandlerTestDB(t)
	user := seedForumLikeTestUser(t, db, "topic-duplicate-unlike")
	topic := seedForumLikeTestTopic(t, db, user)
	like := model.ForumLike{UserID: user.UUID, TargetType: "topic", TargetID: topic.ID}
	if err := db.Create(&like).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Model(&model.ForumTopic{}).Where("id = ?", topic.ID).UpdateColumn("like_count", 1).Error; err != nil {
		t.Fatalf("set topic like_count: %v", err)
	}
	installForumLikeDeleteConflictWithCount(t, db, like)

	r := gin.New()
	r.POST("/topics/:id/like", withForumLikeAuth(user.UUID, (&forumHandler{db: db}).ToggleForumTopicLike()))

	req := httptest.NewRequest(http.MethodPost, "/topics/"+topic.ID.String()+"/like", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	assertForumLikeRows(t, db, "topic", topic.ID, 0)
	assertForumTopicLikeCount(t, db, topic.ID, 0)
}

func TestToggleForumReplyLikeDoesNotDriftOnDuplicateLike(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newForumHandlerTestDB(t)
	user := seedForumLikeTestUser(t, db, "reply-duplicate-like")
	_, reply := seedForumLikeTestReply(t, db, user)
	installForumLikeCreateConflictWithCount(t, db, model.ForumLike{UserID: user.UUID, TargetType: "reply", TargetID: reply.ID})

	r := gin.New()
	r.POST("/replies/:id/like", withForumLikeAuth(user.UUID, (&forumHandler{db: db}).ToggleForumReplyLike()))

	req := httptest.NewRequest(http.MethodPost, "/replies/"+reply.ID.String()+"/like", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	assertForumLikeRows(t, db, "reply", reply.ID, 1)
	assertForumReplyLikeCount(t, db, reply.ID, 1)
}

func TestToggleForumReplyLikeDoesNotDriftOnDuplicateUnlike(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newForumHandlerTestDB(t)
	user := seedForumLikeTestUser(t, db, "reply-duplicate-unlike")
	_, reply := seedForumLikeTestReply(t, db, user)
	like := model.ForumLike{UserID: user.UUID, TargetType: "reply", TargetID: reply.ID}
	if err := db.Create(&like).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Model(&model.ForumReply{}).Where("id = ?", reply.ID).UpdateColumn("like_count", 1).Error; err != nil {
		t.Fatalf("set reply like_count: %v", err)
	}
	installForumLikeDeleteConflictWithCount(t, db, like)

	r := gin.New()
	r.POST("/replies/:id/like", withForumLikeAuth(user.UUID, (&forumHandler{db: db}).ToggleForumReplyLike()))

	req := httptest.NewRequest(http.MethodPost, "/replies/"+reply.ID.String()+"/like", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	assertForumLikeRows(t, db, "reply", reply.ID, 0)
	assertForumReplyLikeCount(t, db, reply.ID, 0)
}

func TestToggleForumTopicBookmarkRejectsMissingTopic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newForumHandlerTestDB(t)
	user := seedForumLikeTestUser(t, db, "bookmark-user")
	missingID := uuid.New()

	r := gin.New()
	r.POST("/topics/:id/bookmark", withForumLikeAuth(user.UUID, ToggleForumTopicBookmark(db)))

	req := httptest.NewRequest(http.MethodPost, "/topics/"+missingID.String()+"/bookmark", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}

	var count int64
	if err := db.Model(&model.ForumBookmark{}).Where("user_id = ? AND topic_id = ?", user.UUID, missingID).Count(&count).Error; err != nil {
		t.Fatalf("count bookmarks: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no bookmark rows for missing topic, got %d", count)
	}
}

func TestReviewCategoryRequestDoesNotPersistApprovalWhenCategoryCreateFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newForumHandlerTestDB(t)
	admin := seedForumLikeTestUser(t, db, "admin-user")
	admin.Role = "admin"
	if err := db.Model(&admin).Update("role", "admin").Error; err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	requester := seedForumLikeTestUser(t, db, "requester")
	category := model.ForumCategory{Name: "Existing", Description: "taken", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create existing category: %v", err)
	}
	request := model.CategoryRequest{
		UserID:      requester.UUID,
		Name:        "Existing",
		Description: "duplicate name",
		Reason:      "please",
		Status:      "pending",
	}
	if err := db.Create(&request).Error; err != nil {
		t.Fatalf("create category request: %v", err)
	}

	r := gin.New()
	r.POST("/category-requests/:id/review", func(c *gin.Context) {
		c.Set("role", "admin")
		c.Set("userID", admin.UUID)
		ReviewCategoryRequest(db)(c)
	})

	body := bytes.NewBufferString(`{"action":"approve"}`)
	req := httptest.NewRequest(http.MethodPost, "/category-requests/"+request.ID.String()+"/review", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}

	var got model.CategoryRequest
	if err := db.First(&got, "id = ?", request.ID).Error; err != nil {
		t.Fatalf("reload category request: %v", err)
	}
	if got.Status != "pending" {
		t.Fatalf("expected request to remain pending after failed category create, got %q", got.Status)
	}
	if got.ReviewedBy != nil {
		t.Fatalf("expected reviewed_by to stay nil after failed approval, got %v", *got.ReviewedBy)
	}
}
