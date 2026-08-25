package blog

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apidocs "atoman/docs"
	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/modules/reference"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func TestMarkdownImportPreviewCreatesCanonicalDraftAndDiagnostics(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	router := newBlogHTTPRouter(service, &user)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "entry.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("---\ntitle: Imported title\nsummary: Imported summary\n---\n# ignored\n\n![remote](https://example.com/image.png)")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/imports/markdown", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	var draft model.ContentBlogDraft
	if err := db.Where("user_id = ?", user.ID).First(&draft).Error; err != nil {
		t.Fatal(err)
	}
	if draft.Title != "Imported title" || draft.Summary != "Imported summary" || !strings.Contains(draft.Content, "https://example.com/image.png") {
		t.Fatalf("unexpected canonical draft: %#v", draft)
	}
	var imported model.BlogMarkdownImport
	if err := db.Where("user_id = ?", user.ID).First(&imported).Error; err != nil {
		t.Fatal(err)
	}
	var diagnostics []model.BlogMarkdownImportDiagnostic
	if err := db.Where("import_id = ?", imported.ID).Find(&diagnostics).Error; err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Code != "external_resource_preserved" {
		t.Fatalf("expected external resource diagnostic, got %#v", diagnostics)
	}
}

func TestMarkdownImportDetailsAndConfirmCanonicalDraft(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "Imported")
	preview, err := service.PreviewMarkdownImport(user, "import.md", []byte("# Imported\n\n![remote](https://example.com/image.png)"))
	if err != nil {
		t.Fatal(err)
	}
	post, err := service.CreatePost(user, CreatePostRequest{ChannelID: channel.ID, CollectionID: collection.ID, Title: preview.Title, Summary: preview.Summary, Content: preview.Content, Status: "draft", Visibility: "public"})
	if err != nil {
		t.Fatal(err)
	}
	router := newBlogHTTPRouter(service, &user)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/blog/imports/markdown/"+preview.ImportID.String(), nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "external_resource_preserved") {
		t.Fatalf("expected import details with diagnostics, got %d: %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	confirmBody := bytes.NewBufferString(`{"content_id":"` + post.ID.String() + `"}`)
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/blog/imports/markdown/"+preview.ImportID.String()+"/confirm", confirmBody))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"confirmed"`) {
		t.Fatalf("expected confirmed import, got %d: %s", response.Code, response.Body.String())
	}
	var entry model.BlogMarkdownImport
	if err := db.First(&entry, "id = ?", preview.ImportID).Error; err != nil {
		t.Fatal(err)
	}
	if entry.ContentID == nil || *entry.ContentID != post.ID || entry.Status != "confirmed" {
		t.Fatalf("unexpected confirmed import: %#v", entry)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/blog/imports/markdown/"+preview.ImportID.String()+"/confirm", bytes.NewBufferString(`{"content_id":"`+post.ID.String()+`"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected idempotent confirmation, got %d: %s", response.Code, response.Body.String())
	}
	var draft model.ContentBlogDraft
	if err := db.First(&draft, "id = ?", preview.DraftID).Error; err != nil {
		t.Fatal(err)
	}
	if draft.ContentID == nil || *draft.ContentID != post.ID {
		t.Fatalf("expected preview draft to be linked to content, got %#v", draft)
	}

	other := model.User{Username: "import-other", Email: "import-other@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	otherUser := authctx.CurrentUser{ID: other.UUID, Username: other.Username, Role: authctx.RoleUser}
	response = httptest.NewRecorder()
	newBlogHTTPRouter(service, &otherUser).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/blog/imports/markdown/"+preview.ImportID.String(), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected other user details 404, got %d: %s", response.Code, response.Body.String())
	}
}

func TestMarkdownImportPreviewRejectsUnsupportedFile(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	router := newBlogHTTPRouter(service, &user)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "entry.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("<h1>not markdown</h1>")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/imports/markdown", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
}

func TestMarkdownExportAllowsAuthorAndRejectsOtherUser(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "Exports")
	asset := model.MediaAsset{UserID: &user.ID, Purpose: "blog.image", URL: "https://assets.example.test/export.png", Key: "blog/export.png", ContentType: "image/png", Size: int64(len("export-asset"))}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	post, err := service.CreatePost(user, CreatePostRequest{ChannelID: channel.ID, CollectionID: collection.ID, Title: "Export", Content: "![image](https://assets.example.test/export.png)", Status: "draft", Visibility: "private"})
	if err != nil {
		t.Fatal(err)
	}
	service.WithExportAssetReader(exportAssetReaderFunc(func(_ context.Context, key string, _ int64) (io.ReadCloser, error) {
		if key != asset.Key {
			t.Fatalf("unexpected key %q", key)
		}
		return io.NopCloser(strings.NewReader("export-asset")), nil
	}))
	response := httptest.NewRecorder()
	newBlogHTTPRouter(service, &user).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String()+"/export", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected author export 200, got %d: %s", response.Code, response.Body.String())
	}
	zipReader, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil || len(zipReader.File) != 3 {
		t.Fatalf("expected exported asset archive, files=%v err=%v", len(zipReader.File), err)
	}

	other := model.User{Username: "export-other", Email: "export-other@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	otherUser := authctx.CurrentUser{ID: other.UUID, Username: other.Username, Role: authctx.RoleUser}
	response = httptest.NewRecorder()
	newBlogHTTPRouter(service, &otherUser).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String()+"/export", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected other user export 403, got %d: %s", response.Code, response.Body.String())
	}
}

func newBlogHTTPTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.UserStudioState{},
		&model.StudioModuleSettings{},
		&model.StudioMetricEvent{},
		&model.Post{},
		&model.PodcastEpisode{},
		&model.Video{},
		&model.ContentPublicationEvent{},
		&model.PostCollection{},
		&model.ContentEntry{},
		&model.ContentPostExtension{},
		&model.ContentBlogExtension{},
		&model.ContentBlogVersion{},
		&model.ContentBlogDraft{},
		&model.BlogMarkdownImport{},
		&model.BlogMarkdownImportDiagnostic{},
		&model.ContentCollection{},
		&model.ContentCollectionMembership{},
		&model.LegacyCollectionMapping{},
		&model.Like{},
		&model.PostRating{},
		&model.Bookmark{},
		&model.BookmarkFolder{},
		&model.AuditLog{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
		&model.ContentReference{},
		&model.MediaAsset{},
		&model.ContentMediaAsset{},
		&model.Notification{},
	)
	if err := migrations.RunNotificationDMIndexes(db); err != nil {
		t.Fatalf("migrate notification indexes: %v", err)
	}
	if err := migrations.RunContentReferencesMigration(db); err != nil {
		t.Fatalf("migrate content references: %v", err)
	}
	postCallbackName := "test:canonical-blog-post-seed-" + uuid.NewString()
	if err := db.Callback().Create().After("gorm:create").Register(postCallbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "posts" {
			return
		}
		var post model.Post
		switch value := tx.Statement.Dest.(type) {
		case *model.Post:
			post = *value
		case model.Post:
			post = value
		default:
			return
		}
		if post.ChannelID == nil || *post.ChannelID == uuid.Nil {
			return
		}
		canonicalizeBlogTestPost(t, tx.Session(&gorm.Session{NewDB: true}), post)
	}); err != nil {
		t.Fatalf("register canonical post test callback: %v", err)
	}
	collectionCallbackName := "test:canonical-blog-collection-seed-" + uuid.NewString()
	if err := db.Callback().Create().After("gorm:create").Register(collectionCallbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "collections" {
			return
		}
		var collection model.Collection
		switch value := tx.Statement.Dest.(type) {
		case *model.Collection:
			collection = *value
		case model.Collection:
			collection = value
		default:
			return
		}
		canonicalizeBlogTestCollection(t, tx.Session(&gorm.Session{NewDB: true}), collection)
	}); err != nil {
		t.Fatalf("register canonical collection test callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Create().Remove(postCallbackName)
		_ = db.Callback().Create().Remove(collectionCallbackName)
	})

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: authctx.RoleUser, DisplayName: "Alice", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
}

func newBlogHTTPRouter(service *Service, current *authctx.CurrentUser) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if current != nil {
			authctx.SetCurrentUser(c, *current)
		}
		c.Next()
	})
	v1 := r.Group("/api/v1")
	RegisterRoutes(v1.Group("/blog"), service)
	return r
}

func TestRegisterRoutesCreatePostRequiresCurrentUser(t *testing.T) {
	service, _, _ := newBlogHTTPTestService(t)
	r := newBlogHTTPRouter(service, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts", bytes.NewBufferString(`{"title":"hello"}`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesUpdateDraftAllowsNoCollection(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel := model.Channel{UserID: &user.ID, Name: "Draft Channel", Slug: "draft-channel-" + uuid.NewString()[:8]}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Before", Content: "body", Status: "draft", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}
	router := newBlogHTTPRouter(service, &user)
	body := bytes.NewBufferString(`{"title":"After","content":"updated","status":"draft","channel_id":"` + channel.ID.String() + `"}`)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected collectionless draft update 200, got %d: %s", response.Code, response.Body.String())
	}
}

func TestRegisterRoutesRejectsPublishingInBannedChannel(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	banUntil := time.Now().UTC().Add(time.Hour)
	channel := model.Channel{
		UserID:   &user.ID,
		Name:     "Banned Channel",
		Slug:     "banned-channel-" + uuid.NewString()[:8],
		BanUntil: &banUntil,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	collection := model.Collection{ChannelID: channel.ID, ContentType: "blog", Name: "Articles"}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	post := model.Post{
		UserID:       user.ID,
		ChannelID:    &channel.ID,
		CollectionID: &collection.ID,
		Title:        "Draft",
		Content:      "body",
		Status:       "draft",
		Visibility:   "public",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}

	router := newBlogHTTPRouter(service, &user)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/publish", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected banned channel publish to return 403, got %d: %s", response.Code, response.Body.String())
	}
	var stored model.Post
	if err := db.First(&stored, "id = ?", post.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "draft" {
		t.Fatalf("expected blocked publish to keep draft status, got %q", stored.Status)
	}
}

func TestRegisterRoutesMountsChannelReadEndpointsAndEnsureDefault(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	secondary := model.Collection{ChannelID: channel.ID, Name: "Featured", Description: "featured"}
	if err := db.Create(&secondary).Error; err != nil {
		t.Fatalf("create secondary collection: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)

	for _, path := range []string{
		"/api/v1/blog/channels",
		"/api/v1/blog/channels?user_id=" + user.ID.String(),
		"/api/v1/blog/channels/" + channel.ID.String(),
		"/api/v1/blog/channels/" + channel.ID.String() + "/collections",
		"/api/v1/blog/channels/slug/" + channel.Slug,
		"/api/v1/blog/channels/slug/" + channel.Slug + "/collections",
		"/api/v1/blog/collections/" + secondary.ID.String(),
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("expected route %s to be mounted, got 404: %s", path, w.Body.String())
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/channels/ensure-default", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected ensure-default route to be mounted, got 404: %s", w.Body.String())
	}
}

func TestRegisterRoutesMountsChannelAndCollectionMutationEndpoints(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	r := newBlogHTTPRouter(service, &user)

	createChannelRaw := bytes.NewBufferString(`{"name":"Studio Channel","slug":"studio-channel","description":"desc"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/channels", createChannelRaw)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected create channel route to be mounted, got 404: %s", w.Body.String())
	}

	var createdChannel struct {
		Data model.Channel `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createdChannel); err != nil {
		t.Fatalf("decode create channel response: %v", err)
	}
	if createdChannel.Data.ID == uuid.Nil {
		t.Fatalf("expected created channel id, got %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/blog/channels/"+createdChannel.Data.ID.String(), bytes.NewBufferString(`{"name":"Studio Channel Updated","slug":"studio-channel-updated"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected update channel route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/blog/channels/"+createdChannel.Data.ID.String()+"/collections", bytes.NewBufferString(`{"name":"Issues","description":"desc"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected create collection route to be mounted, got 404: %s", w.Body.String())
	}

	var createdCollection struct {
		Data model.Collection `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createdCollection); err != nil {
		t.Fatalf("decode create collection response: %v", err)
	}
	if createdCollection.Data.ID == uuid.Nil {
		t.Fatalf("expected created collection id, got %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/blog/collections", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected list user collections route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/blog/collections/"+createdCollection.Data.ID.String(), bytes.NewBufferString(`{"name":"Issues Updated","description":"updated"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected update collection route to be mounted, got 404: %s", w.Body.String())
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.Model(&model.User{}).Where("uuid = ?", user.ID).Update("password", string(passwordHash)).Error; err != nil {
		t.Fatalf("update password: %v", err)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blog/channels/"+createdChannel.Data.ID.String(), bytes.NewBufferString(`{"password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected delete channel route to be mounted, got 404: %s", w.Body.String())
	}
}

func TestCreateChannelCreatesGlobalChannel(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	r := newBlogHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/blog/channels",
		bytes.NewBufferString(`{"name":"Global Channel"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected create channel 201, got %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		Data model.Channel `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.Name != "Global Channel" || payload.Data.ID == uuid.Nil {
		t.Fatalf("expected global channel, got %#v", payload.Data)
	}
	if strings.Contains(w.Body.String(), `"content_type"`) {
		t.Fatalf("expected channel response without module type, got %s", w.Body.String())
	}
}

func TestRegisterRoutesDoesNotMountLegacyChannelArticleRSS(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/channels/slug/"+channel.Slug+"/rss/article", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected legacy article RSS route to be removed, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesMountsBookmarkAndLikeReadEndpoints(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Published", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	missingFolderW := httptest.NewRecorder()
	missingFolderReq := httptest.NewRequest(http.MethodPost, "/api/v1/blog/bookmarks", bytes.NewBufferString(`{"content_id":"`+post.ID.String()+`"}`))
	missingFolderReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(missingFolderW, missingFolderReq)
	if missingFolderW.Code != http.StatusBadRequest {
		t.Fatalf("expected bookmark folder to be required, got %d: %s", missingFolderW.Code, missingFolderW.Body.String())
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String()+"/likes/count", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected likes count route to be mounted, got 404: %s", w.Body.String())
	}

	folder := model.BookmarkFolder{UserID: user.ID, Name: "Favorites"}
	if err := db.Create(&folder).Error; err != nil {
		t.Fatalf("create bookmark folder: %v", err)
	}
	bookmark := model.Bookmark{UserID: user.ID, ContentID: post.ID, BookmarkFolderID: &folder.ID}
	if err := db.Create(&bookmark).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}

	for _, path := range []string{
		"/api/v1/blog/bookmarks",
		"/api/v1/blog/bookmarks?folder_id=" + folder.ID.String(),
		"/api/v1/blog/bookmark-folders",
	} {
		w = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("expected route %s to be mounted, got 404: %s", path, w.Body.String())
		}
	}
}

func TestListBookmarksReturnsPostEngagementCounts(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	post := model.Post{
		UserID: user.ID, ChannelID: &channel.ID, Title: "Bookmarked", Content: "Body", Summary: "Bookmark summary",
		CoverURL: "/covers/bookmarked.jpg", Status: "published", Visibility: "public",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	folder := model.BookmarkFolder{UserID: user.ID, Name: "Favorites"}
	if err := db.Create(&folder).Error; err != nil {
		t.Fatalf("create bookmark folder: %v", err)
	}
	if err := db.Create(&model.Bookmark{UserID: user.ID, ContentID: post.ID, BookmarkFolderID: &folder.ID}).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "post", TargetID: post.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&model.DiscussionTarget{
		Kind: "blog_post", ResourceID: post.ID, ResourceKey: post.ID.String(), CommentCount: 1, RootCount: 1,
	}).Error; err != nil {
		t.Fatalf("create discussion target: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/bookmarks", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []struct {
			ContentID uuid.UUID `json:"content_id"`
			Content   struct {
				ID            uuid.UUID `json:"id"`
				Title         string    `json:"title"`
				Summary       string    `json:"summary"`
				CoverURL      string    `json:"cover_url"`
				LikesCount    int64     `json:"likes_count"`
				CommentsCount int64     `json:"comments_count"`
				User          struct {
					Username string `json:"username"`
				} `json:"user"`
			} `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ContentID != post.ID || payload.Data[0].Content.ID != post.ID {
		t.Fatalf("expected bookmarked content, got %s", w.Body.String())
	}
	if payload.Data[0].Content.Title != post.Title || payload.Data[0].Content.Summary != post.Summary || payload.Data[0].Content.CoverURL != post.CoverURL || payload.Data[0].Content.User.Username != user.Username {
		t.Fatalf("expected original content fields and user, got %s", w.Body.String())
	}
	if payload.Data[0].Content.LikesCount != 1 || payload.Data[0].Content.CommentsCount != 1 {
		t.Fatalf("expected bookmark engagement 1/1, got %d/%d: %s", payload.Data[0].Content.LikesCount, payload.Data[0].Content.CommentsCount, w.Body.String())
	}
}

func TestRegisterRoutesMountsBlogRecommendationPostsEndpoint(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}

	post := model.Post{
		UserID:     user.ID,
		ChannelID:  &channel.ID,
		Title:      "推荐文章",
		Content:    "这是一篇适合推荐的文章内容。",
		Summary:    "推荐摘要",
		Status:     "published",
		Visibility: "public",
		ViewCount:  86,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "post", TargetID: post.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&model.DiscussionTarget{
		Kind: "blog_post", ResourceID: post.ID, ResourceKey: post.ID.String(), CommentCount: 1, RootCount: 1,
	}).Error; err != nil {
		t.Fatalf("create discussion target: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/recommend/posts?mode=hot&page=1&page_size=20", nil)
	r.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("expected recommendation route to be mounted, got 404: %s", w.Body.String())
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			Summary       string `json:"summary"`
			ContentType   string `json:"content_type"`
			TargetPath    string `json:"target_path"`
			ScoreLabel    string `json:"score_label"`
			LikesCount    int64  `json:"likes_count"`
			CommentsCount int64  `json:"comments_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected recommendation items, got %s", w.Body.String())
	}
	first := payload.Data[0]
	if first.LikesCount != 1 || first.CommentsCount != 1 {
		t.Fatalf("expected recommendation engagement 1/1, got %d/%d: %s", first.LikesCount, first.CommentsCount, w.Body.String())
	}
	if first.ID == "" || first.Title == "" || first.TargetPath == "" || first.ScoreLabel == "" || first.ContentType != "blog" {
		t.Fatalf("expected recommendation dto fields, got %#v", first)
	}
	if first.TargetPath != "/post/"+first.ID {
		t.Fatalf("expected canonical post target path, got %q", first.TargetPath)
	}
}

func TestRegisterRoutesMountsBookmarkAndFolderMutationEndpoints(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Published", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	post2 := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Published 2", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&post2).Error; err != nil {
		t.Fatalf("create second post: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/bookmark-folders", bytes.NewBufferString(`{"name":"Favorites"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected create folder route to be mounted, got 404: %s", w.Body.String())
	}

	var folderResp struct {
		Data model.BookmarkFolder `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &folderResp); err != nil {
		t.Fatalf("decode folder response: %v", err)
	}
	if folderResp.Data.ID == uuid.Nil {
		t.Fatalf("expected bookmark folder id, got %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/blog/bookmarks", bytes.NewBufferString(`{"content_id":"`+post2.ID.String()+`","bookmark_folder_id":"`+folderResp.Data.ID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected create bookmark route to be mounted, got 404: %s", w.Body.String())
	}

	var bookmarkResp struct {
		Data BlogBookmarkDTO `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &bookmarkResp); err != nil {
		t.Fatalf("decode bookmark response: %v", err)
	}
	if bookmarkResp.Data.ID == uuid.Nil {
		t.Fatalf("expected bookmark id, got %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/blog/bookmarks", bytes.NewBufferString(`{"content_id":"`+post.ID.String()+`","bookmark_folder_id":"`+folderResp.Data.ID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code >= http.StatusBadRequest {
		t.Fatalf("expected second bookmark create to succeed or be idempotent, got %d: %s", w.Code, w.Body.String())
	}

	var bookmarkForFolderResp struct {
		Data BlogBookmarkDTO `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &bookmarkForFolderResp); err != nil {
		t.Fatalf("decode second bookmark response: %v", err)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blog/bookmarks/"+bookmarkResp.Data.ID.String(), nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected delete bookmark route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blog/bookmark-folders/"+folderResp.Data.ID.String(), nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected delete folder route to be mounted, got 404: %s", w.Body.String())
	}

	var remainingBookmark model.Bookmark
	if err := db.Unscoped().First(&remainingBookmark, "id = ?", bookmarkForFolderResp.Data.ID).Error; err != nil {
		t.Fatalf("reload bookmark: %v", err)
	}
	if remainingBookmark.BookmarkFolderID == nil || *remainingBookmark.BookmarkFolderID == folderResp.Data.ID {
		t.Fatalf("expected bookmark to move to default folder, got %#v", remainingBookmark.BookmarkFolderID)
	}
	var fallback model.BookmarkFolder
	if err := db.First(&fallback, "id = ?", *remainingBookmark.BookmarkFolderID).Error; err != nil || fallback.Name != "默认收藏夹" {
		t.Fatalf("expected default fallback folder, got %#v err=%v", fallback, err)
	}
}

func TestBlogBookmarksSupportPopularSort(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	hotPost := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Hot", Content: "Body", Status: "published", Visibility: "public"}
	coldPost := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Cold", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&hotPost).Error; err != nil {
		t.Fatalf("create hot post: %v", err)
	}
	if err := db.Create(&coldPost).Error; err != nil {
		t.Fatalf("create cold post: %v", err)
	}
	if err := db.Create(&model.Bookmark{UserID: user.ID, ContentID: coldPost.ID}).Error; err != nil {
		t.Fatalf("create cold bookmark: %v", err)
	}
	if err := db.Create(&model.Bookmark{UserID: user.ID, ContentID: hotPost.ID}).Error; err != nil {
		t.Fatalf("create hot bookmark: %v", err)
	}
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "post", TargetID: hotPost.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/bookmarks?sort=popular", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			ContentID string `json:"content_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) < 2 {
		t.Fatalf("expected 2 bookmarks, got %s", w.Body.String())
	}
	if resp.Data[0].ContentID != hotPost.ID.String() {
		t.Fatalf("expected hot post first, got %#v", resp.Data)
	}
}

func TestCreateBookmarkMovesExistingBookmarkToSelectedFolder(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "Move Bookmark")
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Post", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	post.CollectionID = &collection.ID
	canonicalizeBlogTestPost(t, db, post)
	first := model.BookmarkFolder{UserID: user.ID, Name: "First"}
	second := model.BookmarkFolder{UserID: user.ID, Name: "Second"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first folder: %v", err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second folder: %v", err)
	}
	if _, err := service.CreateBookmark(user, post.ID, &first.ID); err != nil {
		t.Fatalf("create bookmark: %v", err)
	}
	bookmark, err := service.CreateBookmark(user, post.ID, &second.ID)
	if err != nil {
		t.Fatalf("move bookmark: %v", err)
	}
	if bookmark.BookmarkFolderID == nil || *bookmark.BookmarkFolderID != second.ID {
		t.Fatalf("expected second folder, got %#v", bookmark.BookmarkFolderID)
	}
}

func TestRegisterRoutesRemovesLegacyCommentsAndKeepsPostLikes(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	r := newBlogHTTPRouter(service, &user)

	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/blog/posts/" + uuid.NewString() + "/comments", ""},
		{http.MethodPost, "/api/v1/blog/posts/" + uuid.NewString() + "/comments", `{"content":"legacy"}`},
		{http.MethodDelete, "/api/v1/blog/comments/" + uuid.NewString(), ""},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected legacy route %s to return 404, got %d", request.path, w.Code)
		}
	}
}

func TestCreateDefaultChannelForUserSkipsReservedAndUserHandles(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	other := model.User{Username: "design", Email: "design@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}

	reservedChannel, err := service.CreateDefaultChannelForUser(user.ID, "feed")
	if err != nil {
		t.Fatalf("create reserved-name channel: %v", err)
	}
	if reservedChannel.Slug == "feed" {
		t.Fatalf("expected reserved feed slug to be skipped")
	}

	userChannel, err := service.CreateDefaultChannelForUser(other.UUID, "design")
	if err != nil {
		t.Fatalf("create username-colliding channel: %v", err)
	}
	if userChannel.Slug == "design" {
		t.Fatalf("expected username-colliding slug to be skipped")
	}
}

func TestCreateDefaultChannelForUserSetsInitialStudioChannel(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)

	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}

	var state model.UserStudioState
	if err := db.First(&state, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("load studio state: %v", err)
	}
	if state.ChannelID == nil || *state.ChannelID != channel.ID {
		t.Fatalf("expected selected channel %s, got %#v", channel.ID, state.ChannelID)
	}
}

func TestCreateDefaultChannelForUserUsesCurrentStudioChannel(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	first := model.Channel{UserID: &user.ID, Name: "First", Slug: "first-" + uuid.NewString()[:8]}
	second := model.Channel{UserID: &user.ID, Name: "Current", Slug: "current-" + uuid.NewString()[:8]}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first channel: %v", err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create current channel: %v", err)
	}
	if err := db.Create(&model.UserStudioState{UserID: user.ID, ChannelID: &second.ID}).Error; err != nil {
		t.Fatalf("create studio state: %v", err)
	}

	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("resolve studio channel: %v", err)
	}
	if channel.ID != second.ID {
		t.Fatalf("expected current studio channel %s, got %s", second.ID, channel.ID)
	}
}

func TestCreateDefaultChannelForUserCreatesTypedBlogCollection(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel := model.Channel{UserID: &user.ID, Name: "Global", Slug: "global-" + uuid.NewString()[:8]}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := db.Create(&model.UserStudioState{UserID: user.ID, ChannelID: &channel.ID}).Error; err != nil {
		t.Fatalf("create studio state: %v", err)
	}
	podcastCollection := model.Collection{
		ChannelID: channel.ID, ContentType: "podcast", Name: "Podcast Default", IsDefault: true,
	}
	if err := db.Create(&podcastCollection).Error; err != nil {
		t.Fatalf("create podcast collection: %v", err)
	}

	if _, err := service.CreateDefaultChannelForUser(user.ID, "Alice"); err != nil {
		t.Fatalf("ensure blog defaults: %v", err)
	}
	var blogCollection model.ContentCollection
	if err := db.Where("channel_id = ? AND is_default = ?", channel.ID, true).First(&blogCollection).Error; err != nil {
		t.Fatalf("expected blog default collection: %v", err)
	}
}

func TestRegisterRoutesCreatePostRejectsInvalidJSON(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	r := newBlogHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts", bytes.NewBufferString(`{"title":`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "validation.invalid_request") {
		t.Fatalf("expected validation.invalid_request, got %s", w.Body.String())
	}
}

func TestRegisterRoutesCreatePostReturnsCreatedPost(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	defaultCollectionName := ensureDefaultCollectionName()
	var collection model.ContentCollection
	if err := service.db.Where("channel_id = ? AND name = ?", channel.ID, defaultCollectionName).First(&collection).Error; err != nil {
		t.Fatalf("load default collection: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{
		"title":         "HTTP Post",
		"content":       "content",
		"excerpt":       "summary",
		"cover_url":     "https://example.com/cover.png",
		"collection_id": collection.ID,
		"visibility":    "public",
		"status":        "draft",
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data model.Post `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ID.String() == "00000000-0000-0000-0000-000000000000" || resp.Data.Title != "HTTP Post" || resp.Data.UserID != user.ID {
		t.Fatalf("unexpected created post: %#v", resp.Data)
	}
	if resp.Data.ChannelID == nil || *resp.Data.ChannelID != channel.ID {
		t.Fatalf("expected channel id %s, got %#v", channel.ID, resp.Data.ChannelID)
	}

	if resp.Data.CollectionID == nil || *resp.Data.CollectionID != collection.ID {
		t.Fatalf("expected created post to be assigned to collection %s, got %#v", collection.ID, resp.Data.CollectionID)
	}
}

func TestBlogPostCRUDUsesCanonicalTablesOnly(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "Canonical CRUD")
	post, err := service.CreatePost(user, CreatePostRequest{
		ChannelID: channel.ID, CollectionID: collection.ID,
		Title: "Canonical title", Content: "canonical body", Status: "draft", Visibility: "public",
	})
	if err != nil {
		t.Fatalf("create canonical post: %v", err)
	}
	var entry model.ContentEntry
	if err := db.First(&entry, "id = ? AND kind = ?", post.ID, "blog").Error; err != nil {
		t.Fatalf("load canonical entry: %v", err)
	}
	var extension model.ContentBlogExtension
	if err := db.First(&extension, "content_id = ?", post.ID).Error; err != nil {
		t.Fatalf("load canonical extension: %v", err)
	}
	var membership model.ContentCollectionMembership
	if err := db.First(&membership, "content_id = ? AND collection_id = ?", post.ID, collection.ID).Error; err != nil {
		t.Fatalf("load canonical membership: %v", err)
	}
	var legacyCount int64
	if err := db.Model(&model.Post{}).Where("id = ?", post.ID).Count(&legacyCount).Error; err != nil {
		t.Fatalf("count legacy posts: %v", err)
	}
	if legacyCount != 0 {
		t.Fatalf("expected no legacy post row after canonical create, got %d", legacyCount)
	}

	router := newBlogHTTPRouter(service, &user)
	body := bytes.NewBufferString(`{"title":"Updated canonical title","content":"updated canonical body","status":"draft","visibility":"private"}`)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update canonical post: %d %s", response.Code, response.Body.String())
	}
	updated, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("reload canonical post: %v", err)
	}
	if updated.Title != "Updated canonical title" || updated.Content != "updated canonical body" || updated.Visibility != "private" {
		t.Fatalf("unexpected canonical update: %#v", updated)
	}
	if err := db.Model(&model.Post{}).Where("id = ?", post.ID).Count(&legacyCount).Error; err != nil {
		t.Fatalf("recount legacy posts: %v", err)
	}
	if legacyCount != 0 {
		t.Fatalf("expected no legacy post row after canonical update, got %d", legacyCount)
	}

	readResponse := httptest.NewRecorder()
	router.ServeHTTP(readResponse, httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil))
	if readResponse.Code != http.StatusOK || !strings.Contains(readResponse.Body.String(), "Updated canonical title") {
		t.Fatalf("canonical detail read did not return updated content: %d %s", readResponse.Code, readResponse.Body.String())
	}
}

func TestLoadCanonicalBlogContentsUsesCanonicalRuntimeType(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "Canonical loader")
	post, err := service.CreatePost(user, CreatePostRequest{
		ChannelID: channel.ID, CollectionID: collection.ID, Title: "Canonical loader title", Content: "canonical loader body", Status: "draft", Visibility: "private",
	})
	if err != nil {
		t.Fatalf("create canonical post: %v", err)
	}

	contents, err := LoadCanonicalBlogContents(db, CanonicalBlogPostsQuery(db).Where("posts.id = ?", post.ID))
	if err != nil {
		t.Fatalf("load canonical contents: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("expected one canonical content, got %d", len(contents))
	}
	content := contents[0]
	if content.ID != post.ID || content.Title != "Canonical loader title" || content.Content != "canonical loader body" {
		t.Fatalf("unexpected canonical content: %#v", content)
	}
	if content.CollectionID == nil || *content.CollectionID != collection.ID || content.Collection == nil || content.Collection.ID != collection.ID {
		t.Fatalf("expected canonical collection projection, got %#v", content.Collection)
	}
	var legacyCount int64
	if err := db.Model(&model.Post{}).Where("id = ?", post.ID).Count(&legacyCount).Error; err != nil {
		t.Fatalf("count legacy posts: %v", err)
	}
	if legacyCount != 0 {
		t.Fatalf("canonical loader must not require a legacy post row, got %d", legacyCount)
	}
}

func TestCreateDraftAllowsIncompleteReferenceAndPublishValidatesIt(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	_, collection := createOwnedChannelAndCollection(t, service, user, "References")
	post, err := service.CreatePost(user, CreatePostRequest{
		Title: "Draft reference", Content: "unfinished @post:", CollectionID: collection.ID, Status: "draft",
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	router := newBlogHTTPRouter(service, &user)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/publish", nil))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid reference publish to return 400, got %d: %s", response.Code, response.Body.String())
	}
	var reloaded model.ContentEntry
	if err := db.First(&reloaded, "id = ?", post.ID).Error; err != nil {
		t.Fatalf("reload canonical entry: %v", err)
	}
	if reloaded.Status != "draft" {
		t.Fatalf("expected failed publish to keep draft status, got %s", reloaded.Status)
	}
}

func TestPublishedPostPersistsAndReturnsReferences(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "References")
	content := "See @channel:" + channel.ID.String()
	router := newBlogHTTPRouter(service, &user)
	body, _ := json.Marshal(map[string]any{
		"title": "Reference post", "content": content, "collection_id": collection.ID, "status": "published",
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	var decoded struct {
		Data struct {
			ID         uuid.UUID                     `json:"id"`
			References []reference.ResolvedReference `json:"references"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.Data.References) != 1 || decoded.Data.References[0].TargetID != channel.ID || !decoded.Data.References[0].Available {
		t.Fatalf("unexpected references: %s", response.Body.String())
	}
	var rows []model.ContentReference
	if err := db.Find(&rows, "source_type = ? AND source_id = ?", "post", decoded.Data.ID).Error; err != nil {
		t.Fatalf("load references: %v", err)
	}
	if len(rows) != 1 || rows[0].SourceField != "content" || rows[0].TargetID != channel.ID {
		t.Fatalf("unexpected stored references: %#v", rows)
	}
}

func TestUpdatePublishedPostReplacesReferences(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "References")
	post, err := service.CreatePost(user, CreatePostRequest{
		Title: "Reference post", Content: "See @channel:" + channel.ID.String(), CollectionID: collection.ID, Status: "published",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"title": "Reference post", "content": "See @collection:" + collection.ID.String(),
		"collection_id": collection.ID.String(), "status": "published",
	})
	request := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	newBlogHTTPRouter(service, &user).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var decoded struct {
		Data struct {
			References []reference.ResolvedReference `json:"references"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.Data.References) != 1 || decoded.Data.References[0].TargetID != collection.ID {
		t.Fatalf("unexpected response references: %s", response.Body.String())
	}
	var rows []model.ContentReference
	if err := db.Find(&rows, "source_type = ? AND source_id = ?", "post", post.ID).Error; err != nil {
		t.Fatalf("load references: %v", err)
	}
	if len(rows) != 1 || rows[0].TargetID != collection.ID {
		t.Fatalf("unexpected stored references: %#v", rows)
	}
}

func TestUnpublishPostRemovesReferences(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "References")
	post, err := service.CreatePost(user, CreatePostRequest{
		Title: "Reference post", Content: "See @channel:" + channel.ID.String(), CollectionID: collection.ID, Status: "published",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	response := httptest.NewRecorder()
	newBlogHTTPRouter(service, &user).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/unpublish", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&model.ContentReference{}).Where("source_type = ? AND source_id = ?", "post", post.ID).Count(&count).Error; err != nil {
		t.Fatalf("count references: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected references removed, got %d", count)
	}
}

func TestDeletePostRemovesReferences(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "References")
	post, err := service.CreatePost(user, CreatePostRequest{
		Title: "Reference post", Content: "See @channel:" + channel.ID.String(), CollectionID: collection.ID, Status: "published",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	response := httptest.NewRecorder()
	newBlogHTTPRouter(service, &user).ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/blog/posts/"+post.ID.String(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&model.ContentReference{}).Where("source_type = ? AND source_id = ?", "post", post.ID).Count(&count).Error; err != nil {
		t.Fatalf("count references: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected references removed, got %d", count)
	}
}

func TestPostListAndDetailReturnReferences(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "References")
	post, err := service.CreatePost(user, CreatePostRequest{
		Title: "Reference post", Content: "See @channel:" + channel.ID.String(), CollectionID: collection.ID, Status: "published",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	router := newBlogHTTPRouter(service, &user)
	for _, path := range []string{"/api/v1/blog/posts", "/api/v1/blog/posts/" + post.ID.String()} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"references":[{"kind":"resource","target_type":"channel"`) {
			t.Fatalf("expected references from %s, got %d: %s", path, response.Code, response.Body.String())
		}
	}
}

func TestRegisterRoutesCreatePostUsesSingleCanonicalCollection(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "Single Collection")
	r := newBlogHTTPRouter(service, &user)

	validBody, _ := json.Marshal(map[string]any{
		"title":         "Valid",
		"content":       "body",
		"channel_id":    channel.ID,
		"collection_id": collection.ID,
		"status":        "draft",
	})
	validReq := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts", bytes.NewReader(validBody))
	validReq.Header.Set("Content-Type", "application/json")
	validW := httptest.NewRecorder()
	r.ServeHTTP(validW, validReq)
	if validW.Code != http.StatusCreated {
		t.Fatalf("expected single collection create to succeed, got %d: %s", validW.Code, validW.Body.String())
	}
	var response struct {
		Data struct {
			CollectionID uuid.UUID `json:"collection_id"`
			ChannelID    uuid.UUID `json:"channel_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(validW.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.CollectionID != collection.ID || response.Data.ChannelID != channel.ID {
		t.Fatalf("expected collection %s and channel %s, got %s", collection.ID, channel.ID, validW.Body.String())
	}
}

func TestCreatePublishedPostRollsBackWhenVersionSnapshotFails(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	_, collection := createOwnedChannelAndCollection(t, service, user, "Alice")
	callbackName := "test:fail-blog-post-version-insert"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "content_blog_versions" {
			tx.AddError(errors.New("version failed"))
		}
	}); err != nil {
		t.Fatalf("register version insert failure: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	_, err := service.CreatePost(user, CreatePostRequest{
		Title:        "Should Roll Back",
		Content:      "content",
		CollectionID: collection.ID,
		Status:       "published",
	})
	if err == nil {
		t.Fatal("expected create post to fail")
	}

	var count int64
	if err := db.Model(&model.ContentEntry{}).Where("title = ?", "Should Roll Back").Count(&count).Error; err != nil {
		t.Fatalf("count posts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected post insert rolled back, count=%d", count)
	}
}

func TestRegisterRoutesCreatePostAcceptsSummaryField(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	_, collection := createOwnedChannelAndCollection(t, service, user, "Alice")

	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{
		"title":         "HTTP Post",
		"content":       "content",
		"summary":       "summary from frontend",
		"collection_id": collection.ID,
		"visibility":    "public",
		"status":        "draft",
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data model.Post `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Summary != "summary from frontend" {
		t.Fatalf("expected created post summary from summary field, got %#v", resp.Data.Summary)
	}
}

func TestRegisterRoutesListPostsReturnsPublishedPosts(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Published", Content: "body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create published post: %v", err)
	}
	canonicalizeBlogTestPost(t, db, post)

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesListPostsOrdersLatestByFirstPublishedAt(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	earlyCreated := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	lateCreated := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	earlyPublished := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	latePublished := time.Date(2026, 7, 5, 8, 0, 0, 0, time.UTC)
	first := model.Post{Base: model.Base{ID: uuid.New(), CreatedAt: earlyCreated}, UserID: user.ID, ChannelID: &channel.ID, Title: "Early created, late published", Content: "body", Status: "published", Visibility: "public", PublishedAt: &earlyPublished}
	second := model.Post{Base: model.Base{ID: uuid.New(), CreatedAt: lateCreated}, UserID: user.ID, ChannelID: &channel.ID, Title: "Late created, early published", Content: "body", Status: "published", Visibility: "public", PublishedAt: &latePublished}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first post: %v", err)
	}
	canonicalizeBlogTestPost(t, db, first)
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second post: %v", err)
	}
	canonicalizeBlogTestPost(t, db, second)

	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts?page=1&page_size=20", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []model.Post `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 2 || response.Data[0].ID != first.ID {
		t.Fatalf("expected late-published post first, got %s", w.Body.String())
	}
}

func TestRegisterRoutesListPostsReturnsPagedFlatDTOWithInteractionCounts(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	for _, title := range []string{"Needle One", "Needle Two", "Needle Three"} {
		post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: title, Content: "body", Status: "published", Visibility: "public"}
		if err := db.Create(&post).Error; err != nil {
			t.Fatalf("create published post: %v", err)
		}
		canonicalizeBlogTestPost(t, db, post)
	}
	var newest model.Post
	if err := db.Where("title = ?", "Needle Three").First(&newest).Error; err != nil {
		t.Fatalf("load newest post: %v", err)
	}
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "post", TargetID: newest.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&model.DiscussionTarget{Kind: "blog_post", ResourceID: newest.ID, ResourceKey: newest.ID.String(), CommentCount: 1, RootCount: 1}).Error; err != nil {
		t.Fatalf("create discussion target: %v", err)
	}

	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts?page=1&page_size=2&q=Needle", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []struct {
			ID            uuid.UUID `json:"id"`
			Title         string    `json:"title"`
			LikesCount    int64     `json:"likes_count"`
			CommentsCount int64     `json:"comments_count"`
		} `json:"data"`
		Meta struct {
			Page     int   `json:"page"`
			PageSize int   `json:"page_size"`
			Total    int64 `json:"total"`
			HasMore  bool  `json:"has_more"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 2 || response.Meta.Page != 1 || response.Meta.PageSize != 2 || response.Meta.Total != 3 || !response.Meta.HasMore {
		t.Fatalf("unexpected paged response: %s", w.Body.String())
	}
	if response.Data[0].ID != newest.ID || response.Data[0].LikesCount != 1 || response.Data[0].CommentsCount != 1 {
		t.Fatalf("expected flat newest post DTO with counts, got %s", w.Body.String())
	}
}

func TestRegisterRoutesListPostsHidesNonPublicPostsFromAnonymousViewer(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	for _, input := range []struct {
		title      string
		visibility string
	}{
		{title: "Public", visibility: "public"},
		{title: "Private", visibility: "private"},
		{title: "Followers", visibility: "followers"},
	} {
		post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: input.title, Content: "body", Status: "published", Visibility: input.visibility}
		if err := db.Create(&post).Error; err != nil {
			t.Fatalf("create post: %v", err)
		}
		canonicalizeBlogTestPost(t, db, post)
	}

	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts?page=1&page_size=20", nil)
	r.ServeHTTP(w, req)

	var response struct {
		Data []model.Post `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].Title != "Public" || response.Meta.Total != 1 {
		t.Fatalf("expected only public post, got %s", w.Body.String())
	}
}

func TestRegisterRoutesGetPostRejectsPrivatePostForNonOwner(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	viewer := model.User{Username: "viewer", Email: "viewer@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&viewer).Error; err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	_, collection := createOwnedChannelAndCollection(t, service, owner, "Alice")
	post, err := service.CreatePost(owner, CreatePostRequest{Title: "Secret", Content: "body", CollectionID: collection.ID, Visibility: "private", Status: "published"})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	r := newBlogHTTPRouter(service, &authctx.CurrentUser{ID: viewer.UUID, Username: viewer.Username, Role: authctx.RoleUser})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesGetPostReturnsViewerLikeState(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Published", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	canonicalizeBlogTestPost(t, db, post)
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "post", TargetID: post.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Data struct {
			ID         uuid.UUID `json:"id"`
			Liked      bool      `json:"liked"`
			LikesCount int64     `json:"likes_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode get post response: %v", err)
	}
	if response.Data.ID != post.ID {
		t.Fatalf("expected post id %s, got %s", post.ID, response.Data.ID)
	}
	if !response.Data.Liked {
		t.Fatalf("expected liked=true, got false: %s", w.Body.String())
	}
	if response.Data.LikesCount != 1 {
		t.Fatalf("expected likes_count=1, got %d: %s", response.Data.LikesCount, w.Body.String())
	}
}

func TestStudioBlogReadReturnsPublicStatsWithoutWritingMetrics(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, owner, "Stats")
	post := model.Post{
		UserID: owner.ID, ChannelID: &channel.ID,
		Title: "Stats", Content: "Body", Status: "published", Visibility: "public", ViewCount: 3,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	canonicalizeBlogTestPost(t, db, post)
	post.CollectionID = &collection.ID
	canonicalizeBlogTestPost(t, db, post)
	if err := db.Create(&model.DiscussionTarget{Kind: "blog_post", ResourceID: post.ID, ResourceKey: post.ID.String(), CommentCount: 1, RootCount: 1}).Error; err != nil {
		t.Fatalf("create discussion target: %v", err)
	}
	if err := db.Create(&model.Bookmark{UserID: owner.ID, ContentID: post.ID}).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &channel.ID, Provider: "internal", Category: "blog", Hash: uuid.NewString(), Title: channel.Name}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create feed source: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: owner.ID, FeedSourceID: source.ID}).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			ViewCount             int64 `json:"view_count"`
			CommentsCount         int64 `json:"comments_count"`
			BookmarksCount        int64 `json:"bookmarks_count"`
			ChannelFollowersCount int64 `json:"channel_followers_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.ViewCount != 3 || response.Data.CommentsCount != 1 || response.Data.BookmarksCount != 1 || response.Data.ChannelFollowersCount != 1 {
		t.Fatalf("unexpected stats: %s", w.Body.String())
	}

	ownerRouter := newBlogHTTPRouter(service, &owner)
	ownerW := httptest.NewRecorder()
	ownerRouter.ServeHTTP(ownerW, httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil))
	updated, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("reload canonical post: %v", err)
	}
	if updated.ViewCount != 3 {
		t.Fatalf("expected detail reads to leave view count unchanged, got %d", updated.ViewCount)
	}
	var events []model.StudioMetricEvent
	if err := db.Where("channel_id = ? AND content_type = ? AND content_id = ? AND metric = ?", channel.ID, "blog", post.ID, "view").Find(&events).Error; err != nil {
		t.Fatalf("load view metric events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected detail reads not to write metric events, got %d", len(events))
	}
}

func TestSEOGetPostReturnsPublicMetadataWithoutIncrementingViewCount(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(owner.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	publishedAt := time.Date(2026, time.July, 10, 8, 30, 0, 0, time.UTC)
	post := model.Post{
		UserID: owner.ID, ChannelID: &channel.ID, Title: "Public post", Content: "Body", Summary: "  Concise summary  ",
		CoverURL: "https://cdn.example.com/cover.jpg", Status: "published", Visibility: "public",
		PublishedAt: &publishedAt, ViewCount: 41,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/blog/seo/posts/"+post.ID.String(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			ID          uuid.UUID  `json:"id"`
			Title       string     `json:"title"`
			Description string     `json:"description"`
			ImageURL    string     `json:"image_url"`
			AuthorName  string     `json:"author_name"`
			PublishedAt *time.Time `json:"published_at"`
			UpdatedAt   time.Time  `json:"updated_at"`
			Path        string     `json:"path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.ID != post.ID || response.Data.Title != post.Title {
		t.Fatalf("unexpected identity metadata: %#v", response.Data)
	}
	if response.Data.Description != "Concise summary" || response.Data.ImageURL != post.CoverURL {
		t.Fatalf("unexpected description or image: %#v", response.Data)
	}
	if response.Data.AuthorName != "Alice" || response.Data.Path != "/posts/post/"+post.ID.String() {
		t.Fatalf("unexpected author or path: %#v", response.Data)
	}
	if response.Data.PublishedAt == nil || !response.Data.PublishedAt.Equal(publishedAt) || response.Data.UpdatedAt.IsZero() {
		t.Fatalf("unexpected timestamps: %#v", response.Data)
	}

	updated, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("reload canonical post: %v", err)
	}
	if updated.ViewCount != 41 {
		t.Fatalf("expected SEO read not to increment view count, got %d", updated.ViewCount)
	}
}

func TestSEOGetPostBuildsUnicodeSafeDescriptionFromMarkdown(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(owner.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	content := "# Heading\n\n**bold** [link](https://example.com)\n\n" + strings.Repeat("界", 170)
	post := model.Post{UserID: owner.ID, ChannelID: &channel.ID, Title: "Fallback", Content: content, Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/blog/seo/posts/"+post.ID.String(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Description string `json:"description"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.ContainsAny(response.Data.Description, "#*[]()") {
		t.Fatalf("expected plain-text markdown fallback, got %q", response.Data.Description)
	}
	if !strings.HasPrefix(response.Data.Description, "Heading bold link ") {
		t.Fatalf("unexpected markdown fallback: %q", response.Data.Description)
	}
	if got := len([]rune(response.Data.Description)); got != 160 {
		t.Fatalf("expected 160 Unicode characters, got %d: %q", got, response.Data.Description)
	}
}

func TestSEOGetPostMarkdownFallbackPreservesComparisonOperators(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(owner.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	post := model.Post{
		UserID: owner.ID, ChannelID: &channel.ID, Title: "Comparison",
		Content: "Use 2 < 3 and 5 > 4 with <strong>bold</strong> text.",
		Status:  "published", Visibility: "public",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/blog/seo/posts/"+post.ID.String(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Description string `json:"description"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := "Use 2 < 3 and 5 > 4 with bold text."
	if response.Data.Description != want {
		t.Fatalf("expected %q, got %q", want, response.Data.Description)
	}
}

func TestSEOGetPostHidesNonPublicAndMissingPosts(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	posts := []model.Post{
		{UserID: owner.ID, Title: "Draft", Content: "Body", Status: "draft", Visibility: "public"},
		{UserID: owner.ID, Title: "Followers", Content: "Body", Status: "published", Visibility: "followers"},
		{UserID: owner.ID, Title: "Private", Content: "Body", Status: "published", Visibility: "private"},
	}
	for i := range posts {
		if err := db.Create(&posts[i]).Error; err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}

	r := newBlogHTTPRouter(service, nil)
	ids := []uuid.UUID{posts[0].ID, posts[1].ID, posts[2].ID, uuid.New()}
	for _, id := range ids {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/blog/seo/posts/"+id.String(), nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected hidden post %s to return 404, got %d: %s", id, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "blog.post_not_found") {
			t.Fatalf("expected uniform not-found error for %s, got %s", id, w.Body.String())
		}
	}
}

func TestSEOSitemapFiltersAndOrdersPublicPublishedPosts(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(owner.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	publishedNew := time.Date(2026, time.July, 12, 10, 0, 0, 0, time.UTC)
	publishedOld := time.Date(2026, time.July, 11, 10, 0, 0, 0, time.UTC)
	createdEarlier := time.Date(2026, time.July, 1, 8, 0, 0, 0, time.UTC)
	createdLater := createdEarlier.Add(time.Hour)
	legacyCreated := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	posts := []model.Post{
		{Base: model.Base{CreatedAt: createdEarlier}, UserID: owner.ID, ChannelID: &channel.ID, Title: "Newest A", Content: "Body", Status: "published", Visibility: "public", PublishedAt: &publishedNew},
		{Base: model.Base{CreatedAt: createdLater}, UserID: owner.ID, ChannelID: &channel.ID, Title: "Newest B", Content: "Body", Status: "published", Visibility: "public", PublishedAt: &publishedNew},
		{UserID: owner.ID, ChannelID: &channel.ID, Title: "Older", Content: "Body", Status: "published", Visibility: "public", PublishedAt: &publishedOld},
		{Base: model.Base{CreatedAt: legacyCreated}, UserID: owner.ID, ChannelID: &channel.ID, Title: "Legacy", Content: "Body", Status: "published", Visibility: "public"},
		{UserID: owner.ID, ChannelID: &channel.ID, Title: "Draft", Content: "Body", Status: "draft", Visibility: "public"},
		{UserID: owner.ID, ChannelID: &channel.ID, Title: "Private", Content: "Body", Status: "published", Visibility: "private", PublishedAt: &publishedNew},
	}
	for i := range posts {
		if err := db.Create(&posts[i]).Error; err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
		canonicalizeBlogTestPost(t, db, posts[i])
	}
	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/blog/seo/sitemap", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []struct {
			Path         string    `json:"path"`
			LastModified time.Time `json:"last_modified"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 4 {
		t.Fatalf("expected four public published posts, got %d: %s", len(response.Data), w.Body.String())
	}
	want := []model.Post{posts[1], posts[0], posts[3], posts[2]}
	for i, item := range response.Data {
		if item.Path != "/posts/post/"+want[i].ID.String() || item.LastModified.Sub(want[i].UpdatedAt).Abs() >= time.Microsecond {
			t.Fatalf("unexpected sitemap item %d: %#v", i, item)
		}
	}
}

func TestSEOSitemapQuerySelectsOnlyRequiredColumns(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(owner.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	post := model.Post{
		UserID: owner.ID, ChannelID: &channel.ID, Title: "Large title", Content: strings.Repeat("large content ", 100),
		Summary: "large summary", Status: "published", Visibility: "public",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	posts, err := service.repo.ListPublicPublishedPosts()
	if err != nil {
		t.Fatalf("list sitemap posts: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("expected one post, got %d", len(posts))
	}
	if posts[0].ID != post.ID || posts[0].UpdatedAt.IsZero() {
		t.Fatalf("expected sitemap fields, got %#v", posts[0])
	}
	if posts[0].Title != "" || posts[0].Content != "" || posts[0].Summary != "" {
		t.Fatalf("expected large content fields not to be selected, got %#v", posts[0])
	}
}

func TestSEOSwaggerSuccessResponsesUseDataEnvelope(t *testing.T) {
	var spec struct {
		Paths map[string]struct {
			Get struct {
				Responses map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					} `json:"schema"`
				} `json:"responses"`
			} `json:"get"`
		} `json:"paths"`
		Definitions map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal([]byte(apidocs.SwaggerInfo.ReadDoc()), &spec); err != nil {
		t.Fatalf("decode swagger document: %v", err)
	}

	cases := map[string]string{
		"/api/v1/blog/seo/posts/{id}": "#/definitions/blog.SEOPostResponse",
		"/api/v1/blog/seo/sitemap":    "#/definitions/blog.SEOSitemapResponse",
	}
	for path, wantRef := range cases {
		gotRef := spec.Paths[path].Get.Responses["200"].Schema.Ref
		if gotRef != wantRef {
			t.Fatalf("expected %s success schema %q, got %q", path, wantRef, gotRef)
		}
		definition := strings.TrimPrefix(wantRef, "#/definitions/")
		if _, ok := spec.Definitions[definition].Properties["data"]; !ok {
			t.Fatalf("expected %s definition to contain data envelope", definition)
		}
	}
}

func TestScheduledPostIsOnlyAccessibleAndInteractiveByOwner(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(owner.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	post := createPostRecord(t, db, owner.ID, &channel.ID, "Scheduled", "scheduled")
	canonicalizeBlogTestPost(t, db, post)
	viewer := model.User{Username: "scheduled-viewer", Email: "scheduled-viewer@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&viewer).Error; err != nil {
		t.Fatal(err)
	}

	for name, current := range map[string]*authctx.CurrentUser{
		"anonymous":  nil,
		"other user": {ID: viewer.UUID, Username: viewer.Username, Role: authctx.RoleUser},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil)
			newBlogHTTPRouter(service, current).ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("expected scheduled post to be forbidden, got %d: %s", response.Code, response.Body.String())
			}
		})
	}

	ownerResponse := httptest.NewRecorder()
	ownerRequest := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil)
	newBlogHTTPRouter(service, &owner).ServeHTTP(ownerResponse, ownerRequest)
	if ownerResponse.Code != http.StatusOK {
		t.Fatalf("expected owner preview to succeed, got %d: %s", ownerResponse.Code, ownerResponse.Body.String())
	}

	other := authctx.CurrentUser{ID: viewer.UUID, Username: viewer.Username, Role: authctx.RoleUser}
	if err := service.ToggleLike(other, "post", post.ID, true); apperr.FromError(err).HTTPStatus != http.StatusForbidden {
		t.Fatalf("expected scheduled post like to be forbidden, got %v", err)
	}
	folder := model.BookmarkFolder{UserID: viewer.UUID, Name: "Scheduled test"}
	if err := db.Create(&folder).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateBookmark(other, post.ID, &folder.ID); apperr.FromError(err).HTTPStatus != http.StatusForbidden {
		t.Fatalf("expected scheduled post bookmark to be forbidden, got %v", err)
	}
}

func TestDeleteChannelAndCollectionRejectNonEmptyResources(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, owner, "Protected")
	post := createPostRecord(t, db, owner.ID, &channel.ID, "Keep me", "published")
	post.CollectionID = &collection.ID
	canonicalizeBlogTestPost(t, db, post)

	if err := service.DeleteCollection(owner, collection.ID); apperr.FromError(err).HTTPStatus != http.StatusConflict {
		t.Fatalf("expected non-empty collection conflict, got %v", err)
	}
	if err := service.DeleteChannel(owner, channel.ID); apperr.FromError(err).HTTPStatus != http.StatusConflict {
		t.Fatalf("expected non-empty channel conflict, got %v", err)
	}
	if err := db.First(&channel, "id = ?", channel.ID).Error; err != nil {
		t.Fatalf("expected channel to remain: %v", err)
	}
	var canonicalCollection model.ContentCollection
	if err := db.First(&canonicalCollection, "id = ?", collection.ID).Error; err != nil {
		t.Fatalf("expected collection to remain: %v", err)
	}
}

func createOwnedChannelAndCollection(t *testing.T, service *Service, user authctx.CurrentUser, name string) (model.Channel, model.Collection) {
	t.Helper()

	channel, err := service.CreateDefaultChannelForUser(user.ID, name)
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}

	var canonical model.ContentCollection
	if err := service.db.Where("channel_id = ? AND is_default = ?", channel.ID, true).First(&canonical).Error; err != nil {
		t.Fatalf("load default collection: %v", err)
	}
	return channel, blogCollectionDTO(canonical)
}

func canonicalizeBlogTestCollection(t *testing.T, db *gorm.DB, collection model.Collection) {
	t.Helper()
	canonical := model.ContentCollection{
		Base:        collection.Base,
		ChannelID:   collection.ChannelID,
		CreatedBy:   collection.CreatedBy,
		Name:        collection.Name,
		Description: collection.Description,
		CoverURL:    collection.CoverURL,
		IsDefault:   collection.IsDefault,
	}
	var existing model.ContentCollection
	result := db.First(&existing, "id = ?", collection.ID)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		if err := db.Create(&canonical).Error; err != nil {
			t.Fatalf("create canonical blog collection: %v", err)
		}
		return
	}
	if result.Error != nil {
		t.Fatalf("load canonical blog collection: %v", result.Error)
	}
	if err := db.Model(&existing).Updates(map[string]any{
		"channel_id": collection.ChannelID, "created_by": collection.CreatedBy,
		"name": collection.Name, "description": collection.Description,
		"cover_url": collection.CoverURL, "is_default": collection.IsDefault,
	}).Error; err != nil {
		t.Fatalf("update canonical blog collection: %v", err)
	}
}

func canonicalizeBlogTestPost(t *testing.T, db *gorm.DB, post model.Post) {
	t.Helper()
	if post.ChannelID == nil || *post.ChannelID == uuid.Nil {
		t.Fatalf("canonical blog post %s has no channel", post.ID)
	}
	entry := model.ContentEntry{
		Base:        post.Base,
		AuthorID:    &post.UserID,
		ChannelID:   *post.ChannelID,
		Kind:        "blog",
		Title:       post.Title,
		Summary:     post.Summary,
		CoverURL:    post.CoverURL,
		Status:      post.Status,
		Visibility:  post.Visibility,
		PublishedAt: post.PublishedAt,
		ScheduledAt: post.ScheduledAt,
	}
	var existing model.ContentEntry
	result := db.First(&existing, "id = ?", post.ID)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		if err := db.Create(&entry).Error; err != nil {
			t.Fatalf("create canonical blog entry: %v", err)
		}
	} else if result.Error != nil {
		t.Fatalf("load canonical blog entry: %v", result.Error)
	} else if err := db.Model(&existing).Updates(map[string]any{
		"author_id": post.UserID, "channel_id": *post.ChannelID, "kind": "blog",
		"title": post.Title, "summary": post.Summary, "cover_url": post.CoverURL,
		"status": post.Status, "visibility": post.Visibility,
		"published_at": post.PublishedAt, "scheduled_at": post.ScheduledAt,
		"updated_at": post.UpdatedAt,
	}).Error; err != nil {
		t.Fatalf("update canonical blog entry: %v", err)
	}
	blogExtension := model.ContentBlogExtension{
		ContentID:          post.ID,
		Content:            post.Content,
		LanguageCode:       post.LanguageCode,
		Pinned:             post.Pinned,
		ViewCount:          post.ViewCount,
		CollectionConflict: post.CollectionConflict,
	}
	var existingExtension model.ContentBlogExtension
	result = db.First(&existingExtension, "content_id = ?", post.ID)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		if err := db.Create(&blogExtension).Error; err != nil {
			t.Fatalf("create canonical blog extension: %v", err)
		}
	} else if result.Error != nil {
		t.Fatalf("load canonical blog extension: %v", result.Error)
	} else if err := db.Model(&existingExtension).Updates(map[string]any{
		"content": post.Content, "language_code": post.LanguageCode,
		"pinned": post.Pinned, "view_count": post.ViewCount,
		"collection_conflict": post.CollectionConflict,
	}).Error; err != nil {
		t.Fatalf("update canonical blog extension: %v", err)
	}
	if err := db.Where("content_id = ?", post.ID).Delete(&model.ContentCollectionMembership{}).Error; err != nil {
		t.Fatalf("clear canonical blog memberships: %v", err)
	}
	if post.CollectionID != nil && *post.CollectionID != uuid.Nil {
		if err := db.Create(&model.ContentCollectionMembership{ContentID: post.ID, CollectionID: *post.CollectionID, Position: post.CollectionPosition}).Error; err != nil {
			t.Fatalf("create canonical blog membership: %v", err)
		}
	}
}

func createPostRecord(t *testing.T, db *gorm.DB, userID uuid.UUID, channelID *uuid.UUID, title, status string) model.Post {
	post := model.Post{UserID: userID, ChannelID: channelID, Title: title, Content: "content", Status: status, Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	return post
}

type testBlogDraftResponse struct {
	ContextKey   string  `json:"context_key"`
	Visibility   string  `json:"visibility"`
	CollectionID *string `json:"collection_id"`
}

func decodePostResponse(t *testing.T, body []byte) model.Post {
	t.Helper()

	var resp struct {
		Data model.Post `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.Data
}

func TestRegisterRoutesUpdatePostUpdatesOwnedPost(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, defaultCollection := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Before", "draft")
	post.CollectionID = &defaultCollection.ID
	canonicalizeBlogTestPost(t, db, post)

	secondary := model.Collection{ChannelID: channel.ID, Name: "Featured", Description: "featured"}
	if err := db.Create(&secondary).Error; err != nil {
		t.Fatalf("create secondary collection: %v", err)
	}
	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{
		"title":          "After",
		"content":        "updated body",
		"summary":        "updated summary",
		"cover_url":      "https://example.com/updated.png",
		"visibility":     "followers",
		"allow_comments": false,
		"status":         "published",
		"collection_id":  secondary.ID.String(),
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	updated := decodePostResponse(t, w.Body.Bytes())
	if updated.Title != "After" || updated.Status != "published" || updated.Visibility != "followers" {
		t.Fatalf("unexpected updated response: %#v", updated)
	}
	if updated.CollectionID == nil || *updated.CollectionID != secondary.ID {
		t.Fatalf("expected selected collection, got %#v", updated.CollectionID)
	}
}

func TestRegisterRoutesUpdatePostRejectsInvalidVisibilityWithoutChangingPrivatePost(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Private", "draft")
	post.Visibility = "private"
	canonicalizeBlogTestPost(t, db, post)

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewBufferString(`{"title":"After","content":"updated body","visibility":"invalid"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	updated, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("reload canonical post: %v", err)
	}
	if updated.Visibility != "private" || updated.Title != "Private" || updated.Content != post.Content {
		t.Fatalf("expected private post to remain unchanged, got %#v", updated)
	}
}

func TestRegisterRoutesUpdatePostVisibilityContract(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     string
		expected string
	}{
		{name: "public", body: `"visibility":"public"`, expected: "public"},
		{name: "followers", body: `"visibility":"followers"`, expected: "followers"},
		{name: "private", body: `"visibility":"private"`, expected: "private"},
		{name: "omitted defaults public", body: "", expected: "public"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, db, user := newBlogHTTPTestService(t)
			channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
			post := createPostRecord(t, db, user.ID, &channel.ID, "Before", "draft")
			post.Visibility = "private"
			canonicalizeBlogTestPost(t, db, post)

			r := newBlogHTTPRouter(service, &user)
			w := httptest.NewRecorder()
			body := `{"title":"After","content":"updated body"`
			if test.body != "" {
				body += "," + test.body
			}
			body += "}"
			req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			if updated := decodePostResponse(t, w.Body.Bytes()); updated.Visibility != test.expected {
				t.Fatalf("expected visibility %q, got %#v", test.expected, updated)
			}
		})
	}
}

func TestRegisterRoutesUpdatePostRejectsOutOfScopeCollectionWithoutChangingPost(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, defaultCollection := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Before", "draft")
	post.CollectionID = &defaultCollection.ID
	canonicalizeBlogTestPost(t, db, post)
	other := model.User{Username: "other-collection-owner", Email: "other-collection@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other owner: %v", err)
	}
	_, foreignCollection := createOwnedChannelAndCollection(t, service, authctx.CurrentUser{ID: other.UUID, Username: other.Username, Role: authctx.RoleUser}, "Other")

	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{"title": "After", "content": "updated body", "collection_id": foreignCollection.ID.String()}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected out-of-scope collection update to return 400, got %d: %s", w.Code, w.Body.String())
	}
	updated, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("reload canonical post: %v", err)
	}
	if updated.Title != "Before" || updated.CollectionID == nil || *updated.CollectionID != defaultCollection.ID {
		t.Fatalf("expected post unchanged, got %#v", updated)
	}
}

func TestRegisterRoutesUpdatePostForbidsNonOwner(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, owner, "Alice")
	post := createPostRecord(t, db, owner.ID, &channel.ID, "Before", "draft")
	viewer := model.User{Username: "viewer2", Email: "viewer2@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&viewer).Error; err != nil {
		t.Fatalf("create viewer: %v", err)
	}

	r := newBlogHTTPRouter(service, &authctx.CurrentUser{ID: viewer.UUID, Username: viewer.Username, Role: authctx.RoleUser})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewBufferString(`{"title":"x","content":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesDeletePostDeletesOwnedPost(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Delete me", "draft")

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/blog/posts/"+post.ID.String(), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var count int64
	if err := db.Model(&model.ContentEntry{}).Where("id = ?", post.ID).Count(&count).Error; err != nil {
		t.Fatalf("count posts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected post deleted, count=%d", count)
	}
}

func TestRegisterRoutesDeletePostForbidsNonOwner(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, owner, "Alice")
	post := createPostRecord(t, db, owner.ID, &channel.ID, "Delete me", "draft")
	viewer := model.User{Username: "viewer3", Email: "viewer3@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&viewer).Error; err != nil {
		t.Fatalf("create viewer: %v", err)
	}

	r := newBlogHTTPRouter(service, &authctx.CurrentUser{ID: viewer.UUID, Username: viewer.Username, Role: authctx.RoleUser})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/blog/posts/"+post.ID.String(), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesPublishPostUpdatesStatus(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	_, collection := createOwnedChannelAndCollection(t, service, user, "Alice")
	post, err := service.CreatePost(user, CreatePostRequest{Title: "Publish me", Content: "body", CollectionID: collection.ID, Status: "draft", Visibility: "public"})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/publish", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	updated, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("reload canonical post: %v", err)
	}
	if updated.Status != "published" || updated.PublishedAt == nil {
		t.Fatalf("expected published with published_at, got %#v", updated)
	}
	var versionCount int64
	if err := db.Model(&model.ContentBlogVersion{}).Where("content_id = ?", post.ID).Count(&versionCount).Error; err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("expected first published version, got %d", versionCount)
	}
	var publicationCount int64
	if err := db.Model(&model.ContentPublicationEvent{}).Where("content_type = ? AND content_id = ?", "blog", post.ID).Count(&publicationCount).Error; err != nil {
		t.Fatal(err)
	}
	if publicationCount != 1 {
		t.Fatalf("expected one blog publication event, got %d", publicationCount)
	}
}

func TestRegisterRoutesUnpublishPostUpdatesStatus(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Unpublish me", "published")

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/unpublish", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	updated, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("reload canonical post: %v", err)
	}
	if updated.Status != "draft" {
		t.Fatalf("expected draft, got %s", updated.Status)
	}
}

func TestRegisterRoutesArchiveAndUnarchivePost(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Archive me", "published")
	r := newBlogHTTPRouter(service, &user)

	for _, action := range []struct {
		path   string
		status string
	}{
		{path: "archive", status: "archived"},
		{path: "unarchive", status: "draft"},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/"+action.path, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", action.path, w.Code, w.Body.String())
		}
		content, err := loadCanonicalBlogContent(db, post.ID)
		if err != nil {
			t.Fatalf("reload canonical content after %s: %v", action.path, err)
		}
		if content.Status != action.status {
			t.Fatalf("status after %s = %q, want %q", action.path, content.Status, action.status)
		}
	}
}

func TestRegisterRoutesAdminCanUnpublishAnotherUsersPost(t *testing.T) {
	service, db, author := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, author, "Author")
	post := createPostRecord(t, db, author.ID, &channel.ID, "Moderated post", "published")
	admin := model.User{Username: "blog-admin", Email: "blog-admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	r := newBlogHTTPRouter(service, &authctx.CurrentUser{ID: admin.UUID, Username: admin.Username, Role: admin.Role})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/unpublish", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	updated, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("reload canonical post: %v", err)
	}
	if updated.Status != "draft" {
		t.Fatalf("expected draft, got %s", updated.Status)
	}
}

func TestRegisterRoutesPinPostUpdatesPinnedState(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Pin me", "published")

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/pin", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	updated, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("reload canonical post: %v", err)
	}
	if !updated.Pinned {
		t.Fatal("expected post pinned")
	}
}

func TestRegisterRoutesUnpinPostUpdatesPinnedState(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Unpin me", "published")
	post.Pinned = true
	canonicalizeBlogTestPost(t, db, post)

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/unpin", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	updated, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("reload canonical post: %v", err)
	}
	if updated.Pinned {
		t.Fatal("expected post unpinned")
	}
}

func TestPublishedPostVersionsPreservePublishedAtAndRestore(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "Versioned")
	post, err := service.CreatePost(user, CreatePostRequest{
		Title: "Version One", Content: "first body", CollectionID: collection.ID, Status: "published", Visibility: "public",
	})
	if err != nil {
		t.Fatalf("create published post: %v", err)
	}
	if post.PublishedAt == nil {
		t.Fatal("expected published_at on first publish")
	}
	firstPublishedAt := *post.PublishedAt

	r := newBlogHTTPRouter(service, &user)
	updateBody, _ := json.Marshal(map[string]any{
		"title": "Version Two", "content": "second body", "collection_id": collection.ID.String(), "status": "published",
	})
	updateW := httptest.NewRecorder()
	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewReader(updateBody))
	updateReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(updateW, updateReq)
	if updateW.Code != http.StatusOK {
		t.Fatalf("update published post: %d %s", updateW.Code, updateW.Body.String())
	}
	updated := decodePostResponse(t, updateW.Body.Bytes())
	if updated.PublishedAt == nil || updated.PublishedAt.Sub(firstPublishedAt).Abs() >= time.Microsecond {
		t.Fatalf("expected published_at to remain %s, got %#v", firstPublishedAt, updated.PublishedAt)
	}

	listW := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String()+"/versions", nil)
	r.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list versions: %d %s", listW.Code, listW.Body.String())
	}
	var listed struct {
		Data []BlogContentVersionDTO `json:"data"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode versions: %v", err)
	}
	if len(listed.Data) != 2 || listed.Data[0].ContentID != post.ID || listed.Data[0].Version != 2 || listed.Data[1].Version != 1 {
		t.Fatalf("expected versions 2 and 1, got %s", listW.Body.String())
	}

	restoreW := httptest.NewRecorder()
	restoreReq := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/versions/1/restore", nil)
	r.ServeHTTP(restoreW, restoreReq)
	if restoreW.Code != http.StatusOK {
		t.Fatalf("restore version: %d %s", restoreW.Code, restoreW.Body.String())
	}
	restored := decodePostResponse(t, restoreW.Body.Bytes())
	if restored.Title != "Version One" || restored.Content != "first body" || restored.PublishedAt == nil || restored.PublishedAt.Sub(firstPublishedAt).Abs() >= time.Microsecond {
		t.Fatalf("unexpected restored post: %#v", restored)
	}
	var versionCount int64
	if err := db.Model(&model.ContentBlogVersion{}).Where("content_id = ?", post.ID).Count(&versionCount).Error; err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versionCount != 3 {
		t.Fatalf("expected restore to create version 3, got %d", versionCount)
	}
	if restored.ChannelID == nil || *restored.ChannelID != channel.ID {
		t.Fatalf("expected channel derived from restored collection, got %#v", restored.ChannelID)
	}
}

func TestRegisterRoutesGetDraftsReturnsUserDrafts(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	_ = createPostRecord(t, db, user.ID, &channel.ID, "Draft one", "draft")
	_ = createPostRecord(t, db, user.ID, &channel.ID, "Published one", "published")

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/drafts", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []model.Post `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Status != "draft" {
		t.Fatalf("unexpected drafts response: %#v", resp.Data)
	}
}

func TestRegisterRoutesGetBlogDraftReturnsSavedDraft(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	draft := model.ContentBlogDraft{UserID: user.ID, ContextKey: "editor:1", Title: "Saved", Content: "body", Visibility: "followers"}
	if err := db.Create(&draft).Error; err != nil {
		t.Fatalf("create draft: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/drafts?context_key=editor:1", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data testBlogDraftResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ContextKey != "editor:1" || resp.Data.Visibility != "followers" {
		t.Fatalf("unexpected blog draft response: %#v", resp.Data)
	}
}

func TestRegisterRoutesPutBlogDraftPersistsFollowersVisibility(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	_, collection := createOwnedChannelAndCollection(t, service, user, "Drafts")
	r := newBlogHTTPRouter(service, &user)
	body := `{"context_key":"editor:2","title":"Draft","content":"body","visibility":"followers","allow_comments":false,"collection_id":"` + collection.ID.String() + `"}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/drafts", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data testBlogDraftResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Visibility != "followers" || resp.Data.CollectionID == nil || *resp.Data.CollectionID != collection.ID.String() {
		t.Fatalf("unexpected saved draft: %#v", resp.Data)
	}
}

func TestRegisterRoutesDeleteBlogDraftRemovesSavedDraft(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	draft := model.ContentBlogDraft{UserID: user.ID, ContextKey: "editor:3", Title: "Saved", Content: "body"}
	if err := db.Create(&draft).Error; err != nil {
		t.Fatalf("create draft: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/blog/drafts?context_key=editor:3", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %s", w.Body.String())
	}

	var count int64
	if err := db.Model(&model.ContentBlogDraft{}).Where("user_id = ? AND context_key = ?", user.ID, "editor:3").Count(&count).Error; err != nil {
		t.Fatalf("count drafts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected draft deleted, count=%d", count)
	}
}

func TestRegisterRoutesDoNotMountLegacyPostCollectionMutationEndpoints(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Collect me", "draft")
	r := newBlogHTTPRouter(service, &user)

	body, _ := json.Marshal(map[string]string{"collection_id": collection.ID.String()})
	addW := httptest.NewRecorder()
	addReq := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/collections", bytes.NewReader(body))
	addReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(addW, addReq)
	if addW.Code != http.StatusNotFound {
		t.Fatalf("expected legacy add route to be absent, got %d: %s", addW.Code, addW.Body.String())
	}

	removeW := httptest.NewRecorder()
	removeReq := httptest.NewRequest(http.MethodDelete, "/api/v1/blog/posts/"+post.ID.String()+"/collections/"+collection.ID.String(), nil)
	r.ServeHTTP(removeW, removeReq)
	if removeW.Code != http.StatusNotFound {
		t.Fatalf("expected legacy remove route to be absent, got %d: %s", removeW.Code, removeW.Body.String())
	}
}

func TestRegisterRoutesReorderCollectionPostsPersistsPosition(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, defaultCollection := createOwnedChannelAndCollection(t, service, user, "Alice")
	postA := createPostRecord(t, db, user.ID, &channel.ID, "Post A", "draft")
	postB := createPostRecord(t, db, user.ID, &channel.ID, "Post B", "published")
	postC := createPostRecord(t, db, user.ID, &channel.ID, "Post C", "draft")
	for position, post := range []*model.Post{&postA, &postB, &postC} {
		post.CollectionID = &defaultCollection.ID
		post.CollectionPosition = position
		canonicalizeBlogTestPost(t, db, *post)
	}

	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{
		"post_ids": []string{postC.ID.String(), postA.ID.String(), postB.ID.String()},
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/collections/"+defaultCollection.ID.String()+"/posts/order", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var memberships []model.ContentCollectionMembership
	if err := db.Where("collection_id = ?", defaultCollection.ID).Order("position ASC").Find(&memberships).Error; err != nil {
		t.Fatalf("reload positions: %v", err)
	}
	if len(memberships) != 3 {
		t.Fatalf("expected 3 posts, got %d", len(memberships))
	}
	got := []uuid.UUID{memberships[0].ContentID, memberships[1].ContentID, memberships[2].ContentID}
	want := []uuid.UUID{postC.ID, postA.ID, postB.ID}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected order %v, got %v", want, got)
		}
		if memberships[i].Position != i {
			t.Fatalf("expected position %d at index %d, got %d", i, i, memberships[i].Position)
		}
	}
}
