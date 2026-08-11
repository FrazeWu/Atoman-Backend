package blog

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/modules/notification"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPublishedPostReferencesAndMentionNotificationsPostgres(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlogReferencePostgresDB(t)
	author := createReferenceTestUser(t, db, "author")
	mentioned := createReferenceTestUser(t, db, "mentioned")

	channel := model.Channel{UserID: &author.UUID, Name: "Reference Channel", Slug: "reference-channel"}
	require.NoError(t, db.Create(&channel).Error)
	collection := model.Collection{ChannelID: channel.ID, ContentType: "blog", CreatedBy: &author.UUID, Name: "Articles"}
	require.NoError(t, db.Create(&collection).Error)
	target := model.Post{
		UserID: author.UUID, ChannelID: &channel.ID, CollectionID: &collection.ID,
		Title: "Referenced post", Content: "body", Status: "published", Visibility: "public",
	}
	require.NoError(t, db.Create(&target).Error)

	authorContext := authctx.CurrentUser{ID: author.UUID, Username: author.Username, Role: author.Role}
	content := fmt.Sprintf("@mentioned @post:%s @author @mentioned", target.ID)
	service := NewService(db)
	post, err := service.CreatePost(authorContext, CreatePostRequest{
		ChannelID: channel.ID, CollectionID: collection.ID,
		Title: "Reference integration", Content: content, Status: "published", Visibility: "public",
	})
	require.NoError(t, err)

	var references []model.ContentReference
	require.NoError(t, db.Where("source_type = ? AND source_id = ?", "post", post.ID).Order("start_offset").Find(&references).Error)
	require.Len(t, references, 4)
	require.Equal(t, []string{"user", "post", "user", "user"}, []string{
		references[0].TargetType, references[1].TargetType, references[2].TargetType, references[3].TargetType,
	})
	require.Equal(t, mentioned.UUID, references[0].TargetID)
	require.Equal(t, target.ID, references[1].TargetID)
	require.Equal(t, author.UUID, references[2].TargetID)
	require.Equal(t, mentioned.UUID, references[3].TargetID)

	notifications := notification.NewService(db)
	mentionedItems, total, err := notifications.ListNotifications(
		authctx.CurrentUser{ID: mentioned.UUID, Username: mentioned.Username, Role: mentioned.Role},
		notification.ListQuery{Page: 1, PageSize: 20, Type: "content_mention"},
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, mentionedItems, 1)
	require.Equal(t, "mention", mentionedItems[0].Category)
	require.Equal(t, "content_reference_post", mentionedItems[0].SourceType)
	require.Equal(t, post.ID.String(), mentionedItems[0].SourceID)
	require.NotNil(t, mentionedItems[0].Actor)
	require.Equal(t, author.UUID.String(), mentionedItems[0].Actor.ID)
	require.Equal(t, "blog", mentionedItems[0].Meta["module"])
	require.Equal(t, "/post/"+post.ID.String(), mentionedItems[0].Meta["path"])

	_, authorNotificationTotal, err := notifications.ListNotifications(authorContext, notification.ListQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Zero(t, authorNotificationTotal)

	router := newBlogHTTPRouter(service, &authorContext)
	body := fmt.Sprintf(
		`{"title":"Reference integration","content":%q,"status":"published","visibility":"public","channel_id":%q,"collection_id":%q}`,
		content, channel.ID, collection.ID,
	)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	_, totalAfterUpdate, err := notifications.ListNotifications(
		authctx.CurrentUser{ID: mentioned.UUID, Username: mentioned.Username, Role: mentioned.Role},
		notification.ListQuery{Page: 1, PageSize: 20, Type: "content_mention"},
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, totalAfterUpdate)
	var referencesAfterUpdate []model.ContentReference
	require.NoError(t, db.Where("source_type = ? AND source_id = ?", "post", post.ID).Find(&referencesAfterUpdate).Error)
	require.Len(t, referencesAfterUpdate, 4)
}

func newBlogReferencePostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("REFERENCE_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://atoman:atoman_secret@localhost:5432/postgres?sslmode=disable"
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	sqlDB, err := admin.DB()
	if err != nil || sqlDB.Ping() != nil {
		t.Skip("PostgreSQL unavailable")
	}

	schema := "blog_reference_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error })

	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{},
		&model.PodcastEpisode{}, &model.BlogPostVersion{}, &model.ContentPublicationEvent{}, &model.ContentReference{},
		&model.Notification{}, &model.NotificationPreference{}, &model.NotificationMute{},
	))
	require.NoError(t, migrations.RunNotificationDMIndexes(db))
	require.NoError(t, migrations.RunContentReferencesMigration(db))
	return db
}

func createReferenceTestUser(t *testing.T, db *gorm.DB, username string) model.User {
	t.Helper()
	user := model.User{
		Username: username, DisplayName: strings.ToUpper(username[:1]) + username[1:],
		Email: username + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true,
	}
	require.NoError(t, db.Create(&user).Error)
	return user
}
