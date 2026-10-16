package app

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"atoman/internal/collab"
	"atoman/internal/handlers"
	"atoman/internal/migrationrunner"
	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 只由前端 scripts/start-rating-acceptance.sh 显式启动，不进入生产二进制。
// 使用独立容器中的测试 schema；退出后 testdb 自动删除 schema。
func TestServeRatingAcceptance(t *testing.T) {
	port := os.Getenv("RATING_ACCEPTANCE_BACKEND_PORT")
	if port == "" {
		t.Skip("仅供独立浏览器验收脚本启动")
	}
	dsn, err := url.Parse(os.Getenv("TEST_POSTGRES_DSN"))
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", dsn.Hostname())
	require.Equal(t, "/rating_acceptance", dsn.Path)
	db := testdb.Open(t)
	require.NoError(t, migrationrunner.Run(db))

	password, err := handlers.HashPassword("rating-acceptance-only")
	require.NoError(t, err)
	now := time.Now()
	users := make([]model.User, 5)
	for i := range users {
		users[i] = model.User{Username: "rating-" + string(rune('a'+i)), Email: "rating-" + string(rune('a'+i)) + "@example.test", Password: password, Role: "user", IsActive: true, OnboardingCompletedAt: &now}
		require.NoError(t, db.Create(&users[i]).Error)
	}
	channel := model.Channel{UserID: &users[0].UUID, Name: "评分验收节目", Slug: "rating-acceptance"}
	require.NoError(t, db.Create(&channel).Error)
	episodes := make(map[string]uuid.UUID)
	for _, state := range []string{"first", "second", "draft", "private"} {
		status, visibility := "published", "public"
		if state == "draft" {
			status = "draft"
		}
		if state == "private" {
			visibility = "private"
		}
		entry := model.ContentEntry{AuthorID: &users[0].UUID, ChannelID: channel.ID, Kind: "podcast", Title: "评分验收 · " + state, Status: status, Visibility: visibility, PublishedAt: &now}
		require.NoError(t, db.Create(&entry).Error)
		id := uuid.New()
		extension := model.ContentEpisodeExtension{ContentID: entry.ID, EpisodeID: id, LegacyPostID: entry.ID, AudioURL: "/api/v1/__rating_acceptance/audio", DurationSec: 120, EpisodeCoverURL: "/api/v1/__rating_acceptance/cover.svg", SeasonNumber: 1, EpisodeNumber: 1, Shownotes: "真实评分接口与隔离数据库的浏览器验收。", CreatedAt: now, UpdatedAt: now}
		require.NoError(t, db.Create(&extension).Error)
		episodes[state] = id
	}
	book := model.BookWork{Title: "评分验收图书", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&book).Error)
	edition := model.BookEdition{WorkID: book.ID, Title: book.Title, CoverURL: "/api/v1/__rating_acceptance/cover.svg", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&edition).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.Recovery())
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())
	r.GET("/__rating_acceptance", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"episodes": episodes, "book_id": book.ID, "username": users[0].Username, "password": "rating-acceptance-only"})
	})
	r.GET("/api/v1/__rating_acceptance/cover.svg", func(c *gin.Context) {
		c.Data(http.StatusOK, "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="320" height="320"><rect width="320" height="320" fill="#304d4b"/><circle cx="160" cy="150" r="64" fill="#f0dcb2"/><text x="160" y="270" fill="white" font-family="sans-serif" font-size="24" text-anchor="middle">ATOMAN</text></svg>`))
	})
	stop := make(chan struct{}, 1)
	r.POST("/__rating_acceptance/stop", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
		select {
		case stop <- struct{}{}:
		default:
		}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	require.NoError(t, err)
	server := &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, server.Shutdown(ctx))
}
