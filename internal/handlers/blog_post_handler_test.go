package handlers

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func newBlogPostTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.PostCollection{},
		&model.BlogDraft{},
		&model.FeedSource{},
		&model.Subscription{},
	)

	return db
}

func seedBlogPostUser(t *testing.T, db *gorm.DB) model.User {
	t.Helper()

	user := model.User{
		Username: "bloguser_" + uuid.NewString()[:8],
		Email:    uuid.NewString() + "@example.com",
		Password: "secret",
		IsActive: true,
	}
	require.NoError(t, db.Create(&user).Error)
	return user
}

func seedBlogPostChannel(t *testing.T, db *gorm.DB, userID uuid.UUID, name string) model.Channel {
	t.Helper()

	channel := model.Channel{UserID: &userID, Name: name, Slug: strings.ToLower(name)}
	require.NoError(t, db.Create(&channel).Error)
	return channel
}

func seedBlogPostChannelSubscription(t *testing.T, db *gorm.DB, subscriberID uuid.UUID, channel model.Channel) {
	t.Helper()

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("internal_channel:"+channel.ID.String())))
	source := model.FeedSource{
		SourceType: "internal_channel",
		SourceID:   &channel.ID,
		Title:      channel.Name,
		Hash:       hash,
	}
	require.NoError(t, db.FirstOrCreate(&source, model.FeedSource{Hash: hash}).Error)
	require.NoError(t, db.Where(model.Subscription{UserID: subscriberID, FeedSourceID: source.ID}).FirstOrCreate(&model.Subscription{
		UserID:       subscriberID,
		FeedSourceID: source.ID,
		Title:        channel.Name,
	}).Error)
}

func withBlogPostAuth(userID uuid.UUID, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("user_id", userID)
		h(c)
	}
}

func TestCreatePostDefaultsVisibilityToPublic(t *testing.T) {
	db := newBlogPostTestDB(t)
	user := seedBlogPostUser(t, db)

	router := gin.New()
	router.POST("/api/blog/posts", withBlogPostAuth(user.UUID, CreatePost(db)))

	body := strings.NewReader(`{"title":"Visibility Default","content":"Post body"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/blog/posts", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var stored struct {
		Visibility string
	}
	result := db.Table("posts").Select("visibility").Where("user_id = ?", user.UUID).Take(&stored)
	require.NoError(t, result.Error)
	require.Equal(t, "public", stored.Visibility)
}

func TestPutBlogDraftPersistsFollowersVisibility(t *testing.T) {
	db := newBlogPostTestDB(t)
	user := seedBlogPostUser(t, db)
	contextKey := "editor:new"

	router := gin.New()
	router.PUT("/api/blog/drafts", withBlogPostAuth(user.UUID, PutBlogDraft(db)))

	body := strings.NewReader(`{"context_key":"editor:new","title":"Draft title","content":"Draft body","visibility":"followers"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/blog/drafts", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var stored struct {
		Visibility string
	}
	result := db.Table("blog_drafts").Select("visibility").Where("user_id = ? AND context_key = ?", user.UUID, contextKey).Take(&stored)
	require.NoError(t, result.Error)
	require.Equal(t, "followers", stored.Visibility)
}

func TestGetPostRejectsPrivatePostForNonOwner(t *testing.T) {
	db := newBlogPostTestDB(t)
	owner := seedBlogPostUser(t, db)
	viewer := seedBlogPostUser(t, db)
	channel := seedBlogPostChannel(t, db, owner.UUID, "private-channel")
	post := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Secret", Content: "body", Status: "published", Visibility: "private", AllowComments: true}
	require.NoError(t, db.Create(&post).Error)

	router := gin.New()
	router.GET("/api/blog/posts/:id", withBlogPostAuth(viewer.UUID, GetPost(db)))

	req := httptest.NewRequest(http.MethodGet, "/api/blog/posts/"+post.ID.String(), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestGetPostAllowsFollowersVisibilityForChannelSubscriber(t *testing.T) {
	db := newBlogPostTestDB(t)
	owner := seedBlogPostUser(t, db)
	follower := seedBlogPostUser(t, db)
	stranger := seedBlogPostUser(t, db)
	channel := seedBlogPostChannel(t, db, owner.UUID, "followers-channel")
	post := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Followers", Content: "body", Status: "published", Visibility: "followers", AllowComments: true}
	require.NoError(t, db.Create(&post).Error)
	seedBlogPostChannelSubscription(t, db, follower.UUID, channel)

	router := gin.New()
	router.GET("/api/blog/posts/:id", withBlogPostAuth(follower.UUID, GetPost(db)))
	req := httptest.NewRequest(http.MethodGet, "/api/blog/posts/"+post.ID.String(), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	strangerRouter := gin.New()
	strangerRouter.GET("/api/blog/posts/:id", withBlogPostAuth(stranger.UUID, GetPost(db)))
	strangerReq := httptest.NewRequest(http.MethodGet, "/api/blog/posts/"+post.ID.String(), nil)
	strangerW := httptest.NewRecorder()
	strangerRouter.ServeHTTP(strangerW, strangerReq)
	require.Equal(t, http.StatusForbidden, strangerW.Code, strangerW.Body.String())
}

func TestUpdatePostWithoutChannelFieldsPreservesExistingChannelAndCollections(t *testing.T) {
	db := newBlogPostTestDB(t)
	user := seedBlogPostUser(t, db)
	channel := seedBlogPostChannel(t, db, user.UUID, "main-channel")
	defaultCollection, err := ensureDefaultCollection(db, channel.ID)
	require.NoError(t, err)

	extraCollection := model.Collection{
		ChannelID: channel.ID,
		Name:      "extra",
	}
	require.NoError(t, db.Create(&extraCollection).Error)

	post := model.Post{
		UserID:        user.UUID,
		ChannelID:     &channel.ID,
		Title:         "Before",
		Content:       "Before body",
		Status:        "draft",
		Visibility:    "public",
		AllowComments: true,
	}
	require.NoError(t, db.Create(&post).Error)
	require.NoError(t, db.Model(&post).Association("Collections").Append(defaultCollection, &extraCollection))

	router := gin.New()
	router.PUT("/api/blog/posts/:id", withBlogPostAuth(user.UUID, UpdatePost(db)))

	body := strings.NewReader(`{"title":"After","content":"After body","summary":"Summary","status":"published"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/blog/posts/"+post.ID.String(), body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var stored model.Post
	require.NoError(t, db.Preload("Collections").First(&stored, "id = ?", post.ID).Error)
	require.NotNil(t, stored.ChannelID)
	require.Equal(t, channel.ID, *stored.ChannelID)
	require.Len(t, stored.Collections, 2)
	require.ElementsMatch(t, []uuid.UUID{defaultCollection.ID, extraCollection.ID}, []uuid.UUID{stored.Collections[0].ID, stored.Collections[1].ID})
}

func TestRequireBlogPostEditAccessRejectsNonOwner(t *testing.T) {
	db := newBlogPostTestDB(t)
	owner := seedBlogPostUser(t, db)
	viewer := seedBlogPostUser(t, db)
	channel := seedBlogPostChannel(t, db, owner.UUID, "collab-channel")
	post := model.Post{
		UserID:        owner.UUID,
		ChannelID:     &channel.ID,
		Title:         "Collab",
		Content:       "body",
		Status:        "draft",
		Visibility:    "private",
		AllowComments: true,
	}
	require.NoError(t, db.Create(&post).Error)

	called := false
	router := gin.New()
	router.GET("/api/collab/ws/:roomID", withBlogPostAuth(viewer.UUID, RequireBlogPostEditAccess(db, func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/collab/ws/"+post.ID.String(), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.False(t, called)
}

func TestRequireBlogPostEditAccessAllowsOwner(t *testing.T) {
	db := newBlogPostTestDB(t)
	owner := seedBlogPostUser(t, db)
	channel := seedBlogPostChannel(t, db, owner.UUID, "collab-owner-channel")
	post := model.Post{
		UserID:        owner.UUID,
		ChannelID:     &channel.ID,
		Title:         "Collab",
		Content:       "body",
		Status:        "draft",
		Visibility:    "private",
		AllowComments: true,
	}
	require.NoError(t, db.Create(&post).Error)

	called := false
	router := gin.New()
	router.GET("/api/collab/ws/:roomID", withBlogPostAuth(owner.UUID, RequireBlogPostEditAccess(db, func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/collab/ws/"+post.ID.String(), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.True(t, called)
}
