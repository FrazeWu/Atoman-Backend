package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/middleware"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func newBlogChannelTestRouter(t *testing.T, dbModels ...any) (*gin.Engine, *gorm.DB, model.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")

	db := testdb.Open(t)
	middleware.SetAuthDB(db)
	models := []any{&model.User{}, &model.Channel{}, &model.Collection{}, &model.FeedSource{}, &model.SubscriptionGroup{}, &model.Subscription{}}
	models = append(models, dbModels...)
	testdb.Migrate(t, db, models...)

	user := model.User{Username: "owner", Email: "owner@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	r := gin.New()
	SetupBlogChannelRoutes(r, db)
	return r, db, user
}

func blogChannelAuthHeader(t *testing.T, user model.User) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": user.Username,
		"role":     user.Role,
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return "Bearer " + signed
}

func TestCreateChannelRejectsReservedSlug(t *testing.T) {
	r, _, user := newBlogChannelTestRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/channels", strings.NewReader(`{"name":"Feed","slug":"feed"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", blogChannelAuthHeader(t, user))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "reserved") {
		t.Fatalf("expected reserved error, got %s", w.Body.String())
	}
}

func TestCreateChannelRejectsSlugMatchingUsername(t *testing.T) {
	r, db, user := newBlogChannelTestRouter(t)
	other := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/channels", strings.NewReader(`{"name":"Alice Channel","slug":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", blogChannelAuthHeader(t, user))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already in use") {
		t.Fatalf("expected already in use error, got %s", w.Body.String())
	}
}

func TestCreateChannelRollsBackWhenDefaultCollectionFails(t *testing.T) {
	r, db, user := newBlogChannelTestRouter(t)
	if err := db.Exec(`
		CREATE TRIGGER fail_default_collection_insert
		BEFORE INSERT ON collections
		BEGIN
			SELECT RAISE(FAIL, 'default collection failed');
		END;
	`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/channels", strings.NewReader(`{"name":"Rollback Channel"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", blogChannelAuthHeader(t, user))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}

	var channelCount int64
	if err := db.Model(&model.Channel{}).Where("name = ?", "Rollback Channel").Count(&channelCount).Error; err != nil {
		t.Fatalf("count channels: %v", err)
	}
	if channelCount != 0 {
		t.Fatalf("expected channel creation to roll back, got %d channel(s)", channelCount)
	}
}

func TestUpdateChannelRejectsSlugMatchingUsername(t *testing.T) {
	r, db, user := newBlogChannelTestRouter(t)
	channel := model.Channel{UserID: &user.UUID, Name: "Original", Slug: "original"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	other := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/channels/"+channel.ID.String(), strings.NewReader(`{"name":"Original","slug":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", blogChannelAuthHeader(t, user))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already in use") {
		t.Fatalf("expected already in use error, got %s", w.Body.String())
	}
}

func TestDeleteChannelMoveContentMigratesPostsOutsideCollections(t *testing.T) {
	r, db, user := newBlogChannelTestRouter(t, &model.Post{}, &model.PostCollection{})
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.Model(&model.User{}).Where("uuid = ?", user.UUID).Update("password", string(passwordHash)).Error; err != nil {
		t.Fatalf("update password: %v", err)
	}

	source := model.Channel{UserID: &user.UUID, Name: "Source", Slug: "source"}
	target := model.Channel{UserID: &user.UUID, Name: "Target", Slug: "target"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source channel: %v", err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target channel: %v", err)
	}

	sourceCollection := model.Collection{ChannelID: source.ID, Name: "Source Default", IsDefault: true}
	if err := db.Create(&sourceCollection).Error; err != nil {
		t.Fatalf("create source collection: %v", err)
	}

	inCollectionPost := model.Post{
		UserID:    user.UUID,
		ChannelID: &source.ID,
		Title:     "Post in collection",
		Content:   "content",
	}
	loosePost := model.Post{
		UserID:    user.UUID,
		ChannelID: &source.ID,
		Title:     "Post without collection",
		Content:   "content",
	}
	if err := db.Create(&inCollectionPost).Error; err != nil {
		t.Fatalf("create collection post: %v", err)
	}
	if err := db.Create(&loosePost).Error; err != nil {
		t.Fatalf("create loose post: %v", err)
	}
	if err := db.Create(&model.PostCollection{PostID: inCollectionPost.ID, CollectionID: sourceCollection.ID}).Error; err != nil {
		t.Fatalf("create source post collection: %v", err)
	}

	body := `{"password":"secret","move_content":true,"target_channel_id":"` + target.ID.String() + `"}`
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/blog/channels/"+source.ID.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", blogChannelAuthHeader(t, user))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var migratedInCollection model.Post
	if err := db.First(&migratedInCollection, "id = ?", inCollectionPost.ID).Error; err != nil {
		t.Fatalf("load migrated collection post: %v", err)
	}
	if migratedInCollection.ChannelID == nil || *migratedInCollection.ChannelID != target.ID {
		t.Fatalf("expected collection post channel %s, got %v", target.ID, migratedInCollection.ChannelID)
	}

	var migratedLoose model.Post
	if err := db.First(&migratedLoose, "id = ?", loosePost.ID).Error; err != nil {
		t.Fatalf("load migrated loose post: %v", err)
	}
	if migratedLoose.ChannelID == nil || *migratedLoose.ChannelID != target.ID {
		t.Fatalf("expected loose post channel %s, got %v", target.ID, migratedLoose.ChannelID)
	}

	var targetDefault model.Collection
	if err := db.Where("channel_id = ? AND is_default = ?", target.ID, true).First(&targetDefault).Error; err != nil {
		t.Fatalf("load target default collection: %v", err)
	}
	var targetRelationCount int64
	if err := db.Model(&model.PostCollection{}).
		Where("post_id = ? AND collection_id = ?", inCollectionPost.ID, targetDefault.ID).
		Count(&targetRelationCount).Error; err != nil {
		t.Fatalf("count target relation: %v", err)
	}
	if targetRelationCount != 1 {
		t.Fatalf("expected collection post to be linked to target default collection, got %d", targetRelationCount)
	}
}
