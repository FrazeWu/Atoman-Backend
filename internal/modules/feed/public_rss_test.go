package feed

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func newPublicRSSTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.ContentEntry{},
		&model.ContentBlogExtension{},
		&model.ContentBlogTag{},
		&model.ContentEpisodeExtension{},
		&model.ContentVideoExtension{},
		&model.ContentCollection{},
		&model.ContentCollectionMembership{},
	)
	return db
}

func TestFeedRoutesDoNotMountLegacyUserRSS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newPublicRSSTestDB(t)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), NewService(db))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/feed/rss/legacy-user", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected legacy user RSS route to return 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPublicRSSScopesPublishedPublicMixedContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newPublicRSSTestDB(t)
	now := time.Date(2026, time.August, 24, 14, 0, 0, 0, time.UTC)
	user := model.User{
		Username: "rss-creator", Email: "rss-creator@example.com", Password: "secret", IsActive: true,
		DisplayName: "RSS Creator",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "Creator Channel", Slug: "creator-channel", Description: "Mixed public work"}
	otherChannel := model.Channel{UserID: &user.UUID, Name: "Elsewhere", Slug: "elsewhere"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := db.Create(&otherChannel).Error; err != nil {
		t.Fatalf("create other channel: %v", err)
	}
	collection := model.ContentCollection{ChannelID: channel.ID, CreatedBy: &user.UUID, Name: "Featured", Description: "Selected work"}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatalf("create collection: %v", err)
	}

	blog := createPublicRSSTestEntry(t, db, user.UUID, channel.ID, "blog", "Public article", "Article summary", "public", "published", now.Add(-2*time.Hour))
	if err := db.Create(&model.ContentBlogExtension{ContentID: blog.ID, Content: "Article body"}).Error; err != nil {
		t.Fatalf("create blog extension: %v", err)
	}
	podcast := createPublicRSSTestEntry(t, db, user.UUID, channel.ID, "podcast", "Public episode", "Episode summary", "public", "published", now.Add(-time.Hour))
	episodeID := uuid.New()
	if err := db.Create(&model.ContentEpisodeExtension{
		ContentID: podcast.ID, EpisodeID: episodeID, AudioURL: "https://cdn.example.com/episode.mp3",
	}).Error; err != nil {
		t.Fatalf("create episode extension: %v", err)
	}
	video := createPublicRSSTestEntry(t, db, user.UUID, channel.ID, "video", "Follower video", "", "followers", "published", now)
	videoID := uuid.New()
	if err := db.Create(&model.ContentVideoExtension{
		ContentID: video.ID, VideoID: videoID, StorageType: "local", VideoURL: "https://cdn.example.com/video.mp4",
	}).Error; err != nil {
		t.Fatalf("create video extension: %v", err)
	}
	createPublicRSSTestEntry(t, db, user.UUID, channel.ID, "blog", "Draft article", "", "public", "draft", now)
	elsewhere := createPublicRSSTestEntry(t, db, user.UUID, otherChannel.ID, "blog", "Elsewhere article", "", "public", "published", now.Add(-30*time.Minute))
	if err := db.Create(&model.ContentBlogExtension{ContentID: elsewhere.ID, Content: "Elsewhere body"}).Error; err != nil {
		t.Fatalf("create elsewhere extension: %v", err)
	}
	memberships := []model.ContentCollectionMembership{
		{ContentID: blog.ID, CollectionID: collection.ID},
		{ContentID: podcast.ID, CollectionID: collection.ID},
	}
	if err := db.Create(&memberships).Error; err != nil {
		t.Fatalf("add collection memberships: %v", err)
	}

	router := gin.New()
	RegisterPublicRSSRoutes(router.Group("/api/v1/rss"), db)

	userFeed := requestPublicRSS(t, router, "/api/v1/rss/users/rss-creator.xml")
	assertPublicRSSItems(t, userFeed, "Public episode", "Elsewhere article", "Public article")
	if strings.Contains(userFeed, "Follower video") || strings.Contains(userFeed, "Draft article") {
		t.Fatalf("user feed leaked non-public or draft content: %s", userFeed)
	}
	if !strings.Contains(userFeed, `enclosure url="https://cdn.example.com/episode.mp3" length="0" type="audio/mpeg"`) {
		t.Fatalf("user feed is missing podcast enclosure: %s", userFeed)
	}
	if !strings.Contains(userFeed, "<content:encoded>Article body</content:encoded>") {
		t.Fatalf("user feed is missing blog full content: %s", userFeed)
	}

	channelFeed := requestPublicRSS(t, router, "/api/v1/rss/channels/creator-channel.xml")
	assertPublicRSSItems(t, channelFeed, "Public episode", "Public article")
	if strings.Contains(channelFeed, "Elsewhere article") {
		t.Fatalf("channel feed included content from another channel: %s", channelFeed)
	}

	collectionFeed := requestPublicRSS(t, router, "/api/v1/rss/collections/"+collection.ID.String()+".xml")
	assertPublicRSSItems(t, collectionFeed, "Public episode", "Public article")
	if strings.Contains(collectionFeed, "Elsewhere article") || strings.Contains(collectionFeed, "Follower video") {
		t.Fatalf("collection feed included content outside its public members: %s", collectionFeed)
	}
}

func TestPublicRSSReturnsNotFoundForUnknownSources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newPublicRSSTestDB(t)
	router := gin.New()
	RegisterPublicRSSRoutes(router.Group("/api/v1/rss"), db)

	for _, path := range []string{
		"/api/v1/rss/users/missing.xml",
		"/api/v1/rss/channels/missing.xml",
		"/api/v1/rss/collections/not-a-uuid.xml",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		writer := httptest.NewRecorder()
		router.ServeHTTP(writer, req)
		if writer.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, writer.Code)
		}
	}
}

func createPublicRSSTestEntry(
	t *testing.T,
	db *gorm.DB,
	authorID, channelID uuid.UUID,
	kind, title, summary, visibility, status string,
	publishedAt time.Time,
) model.ContentEntry {
	t.Helper()
	entry := model.ContentEntry{
		AuthorID: &authorID, ChannelID: channelID, Kind: kind, Title: title, Summary: summary,
		Status: status, Visibility: visibility, PublishedAt: &publishedAt,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create %s entry %q: %v", kind, title, err)
	}
	return entry
}

func requestPublicRSS(t *testing.T, router *gin.Engine, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "atoman.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	writer := httptest.NewRecorder()
	router.ServeHTTP(writer, req)
	if writer.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", path, writer.Code, writer.Body.String())
	}
	if contentType := writer.Header().Get("Content-Type"); !strings.Contains(contentType, "application/rss+xml") {
		t.Fatalf("GET %s content type = %q", path, contentType)
	}
	var document publicRSSDocument
	if err := xml.Unmarshal(writer.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode RSS for %s: %v\n%s", path, err, writer.Body.String())
	}
	if document.Version != "2.0" || document.Channel.LastBuildDate == "" {
		t.Fatalf("invalid RSS document for %s: %+v", path, document)
	}
	return writer.Body.String()
}

func assertPublicRSSItems(t *testing.T, feed string, expectedTitles ...string) {
	t.Helper()
	for _, title := range expectedTitles {
		if !strings.Contains(feed, "<title>"+title+"</title>") {
			t.Fatalf("feed missing %q: %s", title, feed)
		}
	}
}
