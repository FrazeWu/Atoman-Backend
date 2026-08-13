package forum

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/modules/reference"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type forumHTTPResponse struct {
	Data json.RawMessage `json:"data"`
	Meta struct {
		Page     int   `json:"page"`
		PageSize int   `json:"page_size"`
		Total    int64 `json:"total"`
	} `json:"meta"`
}

func newForumHTTPTestRouter(t *testing.T) (*gin.Engine, *gorm.DB, model.User, model.ForumCategory) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumDraft{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentMention{},
		&model.CommentAttachment{},
		&model.CommentLike{},
		&model.CommentReport{},
		&model.CommentTimeAnchor{},
		&model.CommentPublishRecord{},
		&model.ForumLike{},
		&model.ForumBookmark{},
		&model.ForumFollow{},
		&model.ForumReport{},
		&model.ForumUserTrust{},
		&model.ForumGroup{},
		&model.ForumGroupMember{},
		&model.ForumCategoryPermission{},
		&model.ForumUserModerationAction{},
		&model.ForumModeratorAssignment{},
		&model.ContentReference{},
		&model.TimelineRevisionProposal{},
		&model.Notification{},
		&model.Post{},
		&model.PodcastEpisode{},
	)
	if err := migrations.RunNotificationDMIndexes(db); err != nil {
		t.Fatalf("migrate notification indexes: %v", err)
	}
	if err := migrations.RunContentReferencesMigration(db); err != nil {
		t.Fatalf("migrate content references: %v", err)
	}
	user := model.User{Username: "forum-owner", Email: "forum-owner@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	category := model.ForumCategory{Name: "General", Description: "General topics", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role})
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1/forum"), NewService(db))
	return router, db, user, category
}

func performForumRequest(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload *bytes.Reader
	if body == nil {
		payload = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		payload = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, payload)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func decodeForumData[T any](t *testing.T, response *httptest.ResponseRecorder) (T, forumHTTPResponse) {
	t.Helper()
	var envelope forumHTTPResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response envelope: %v: %s", err, response.Body.String())
	}
	var data T
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode response data: %v: %s", err, response.Body.String())
	}
	return data, envelope
}

func TestCreateAndUpdateTopicPersistNormalizedTags(t *testing.T) {
	router, _, _, category := newForumHTTPTestRouter(t)
	response := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/topics", map[string]any{
		"category_id": category.ID,
		"title":       "Tagged topic",
		"content":     "Body",
		"tags":        []string{" Go ", "", "Go", "Vue"},
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	topic, _ := decodeForumData[model.ForumTopic](t, response)
	if got := []string(topic.Tags); len(got) != 2 || got[0] != "Go" || got[1] != "Vue" {
		t.Fatalf("expected normalized tags, got %#v", got)
	}

	response = performForumRequest(t, router, http.MethodPut, "/api/v1/forum/topics/"+topic.ID.String(), map[string]any{
		"title": "Tagged topic", "content": "Body", "tags": []string{" Rust ", "Rust", "SQLite"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	updated, _ := decodeForumData[model.ForumTopic](t, response)
	if got := []string(updated.Tags); len(got) != 2 || got[0] != "Rust" || got[1] != "SQLite" {
		t.Fatalf("expected updated normalized tags, got %#v", got)
	}
}

func TestCreateTopicPersistsAndReturnsReferences(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	response := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/topics", map[string]any{
		"category_id": category.ID, "title": "Reference topic", "content": "Hello @" + user.Username,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	topic, _ := decodeForumData[struct {
		model.ForumTopic
		References []reference.ResolvedReference `json:"references"`
	}](t, response)
	if len(topic.References) != 1 || topic.References[0].TargetID != user.UUID || !topic.References[0].Available {
		t.Fatalf("unexpected references: %s", response.Body.String())
	}
	var rows []model.ContentReference
	if err := db.Find(&rows, "source_type = ? AND source_id = ?", "thread", topic.ID).Error; err != nil {
		t.Fatalf("load references: %v", err)
	}
	if len(rows) != 1 || rows[0].SourceField != "content" || rows[0].TargetID != user.UUID {
		t.Fatalf("unexpected stored references: %#v", rows)
	}
}

func TestCreateTopicRejectsUnavailableReference(t *testing.T) {
	router, db, _, category := newForumHTTPTestRouter(t)
	response := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/topics", map[string]any{
		"category_id": category.ID, "title": "Invalid reference", "content": "See @post:" + uuid.NewString(),
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&model.ForumTopic{}).Where("title = ?", "Invalid reference").Count(&count).Error; err != nil {
		t.Fatalf("count topics: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected invalid topic transaction rolled back, got %d rows", count)
	}
}

func TestUpdateAndDeleteTopicReplaceThenRemoveReferences(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	created := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/topics", map[string]any{
		"category_id": category.ID, "title": "Reference topic", "content": "Hello @" + user.Username,
	})
	topic, _ := decodeForumData[model.ForumTopic](t, created)
	bob := model.User{Username: "forum-bob", Email: "forum-bob@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&bob).Error; err != nil {
		t.Fatalf("create referenced user: %v", err)
	}
	updated := performForumRequest(t, router, http.MethodPut, "/api/v1/forum/topics/"+topic.ID.String(), map[string]any{
		"title": "Reference topic", "content": "Hello @" + bob.Username,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", updated.Code, updated.Body.String())
	}
	updatedTopic, _ := decodeForumData[struct {
		model.ForumTopic
		References []reference.ResolvedReference `json:"references"`
	}](t, updated)
	if len(updatedTopic.References) != 1 || updatedTopic.References[0].TargetID != bob.UUID {
		t.Fatalf("unexpected updated references: %s", updated.Body.String())
	}
	deleted := performForumRequest(t, router, http.MethodDelete, "/api/v1/forum/topics/"+topic.ID.String(), nil)
	if deleted.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", deleted.Code, deleted.Body.String())
	}
	var count int64
	if err := db.Model(&model.ContentReference{}).Where("source_type = ? AND source_id = ?", "thread", topic.ID).Count(&count).Error; err != nil {
		t.Fatalf("count references: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected deleted topic references removed, got %d", count)
	}
}

func TestDeleteTopicRemovesUnifiedCommentsAndTopicRelations(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	created := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/topics", map[string]any{
		"category_id": category.ID, "title": "Delete with comments", "content": "Body",
	})
	topic, _ := decodeForumData[model.ForumTopic](t, created)
	target := model.DiscussionTarget{
		Kind: comment.TargetKindForumTopic, ResourceID: topic.ID, ResourceKey: topic.ID.String(), OwnerID: &user.UUID,
		CommentCount: 1, RootCount: 1, PinnedCommentID: nil,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}
	entry := model.CommentEntry{TargetID: target.ID, AuthorID: user.UUID, Content: "reply", ContentHash: "reply", Status: comment.CommentStatusActive}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create comment: %v", err)
	}
	follow := model.ForumFollow{UserID: user.UUID, TargetType: model.ForumFollowTargetTopic, TargetKey: topic.ID.String()}
	if err := db.Create(&follow).Error; err != nil {
		t.Fatalf("create follow: %v", err)
	}
	bookmark := model.ForumBookmark{UserID: user.UUID, TopicID: topic.ID}
	if err := db.Create(&bookmark).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}
	like := model.ForumLike{UserID: user.UUID, TargetType: "topic", TargetID: topic.ID}
	if err := db.Create(&like).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	notification := model.Notification{RecipientID: user.UUID, Type: "forum_follow", SourceType: "forum_topic", SourceID: topic.ID}
	if err := db.Create(&notification).Error; err != nil {
		t.Fatalf("create notification: %v", err)
	}

	deleted := performForumRequest(t, router, http.MethodDelete, "/api/v1/forum/topics/"+topic.ID.String(), nil)
	if deleted.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", deleted.Code, deleted.Body.String())
	}
	var count int64
	for _, query := range []struct {
		name  string
		model any
		where string
		id    uuid.UUID
	}{
		{"target", &model.DiscussionTarget{}, "id = ?", target.ID},
		{"comment", &model.CommentEntry{}, "id = ?", entry.ID},
		{"follow", &model.ForumFollow{}, "id = ?", follow.ID},
		{"bookmark", &model.ForumBookmark{}, "id = ?", bookmark.ID},
		{"like", &model.ForumLike{}, "id = ?", like.ID},
		{"notification", &model.Notification{}, "id = ?", notification.ID},
	} {
		if err := db.Unscoped().Model(query.model).Where(query.where, query.id).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", query.name, err)
		}
		if count != 0 {
			t.Fatalf("expected deleted %s rows, got %d", query.name, count)
		}
	}
}

func TestTopicListSearchAndDetailReturnReferences(t *testing.T) {
	router, _, user, category := newForumHTTPTestRouter(t)
	created := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/topics", map[string]any{
		"category_id": category.ID, "title": "Reference needle", "content": "Hello @" + user.Username,
	})
	topic, _ := decodeForumData[model.ForumTopic](t, created)
	type topicWithReferences struct {
		model.ForumTopic
		References []reference.ResolvedReference `json:"references"`
	}
	for _, path := range []string{
		"/api/v1/forum/topics", "/api/v1/forum/search?q=needle", "/api/v1/forum/topics/" + topic.ID.String(),
	} {
		response := performForumRequest(t, router, http.MethodGet, path, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("expected 200 from %s, got %d: %s", path, response.Code, response.Body.String())
		}
		if path == "/api/v1/forum/topics/"+topic.ID.String() {
			item, _ := decodeForumData[topicWithReferences](t, response)
			if len(item.References) != 1 || item.References[0].TargetID != user.UUID {
				t.Fatalf("unexpected detail references: %s", response.Body.String())
			}
			continue
		}
		items, _ := decodeForumData[[]topicWithReferences](t, response)
		if len(items) != 1 || len(items[0].References) != 1 || items[0].References[0].TargetID != user.UUID {
			t.Fatalf("unexpected list references from %s: %s", path, response.Body.String())
		}
	}
}

func TestTopicStateIsConsistentAcrossListSearchDetailAndUsers(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	created := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/topics", map[string]any{
		"category_id": category.ID, "title": "State needle", "content": "Topic body",
	})
	topic, _ := decodeForumData[model.ForumTopic](t, created)
	latestAt := time.Now().UTC().Add(-time.Minute)
	target := model.DiscussionTarget{
		Kind: comment.TargetKindForumTopic, ResourceID: topic.ID, ResourceKey: topic.ID.String(),
		CommentCount: 1, RootCount: 1,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create discussion target: %v", err)
	}
	if err := db.Create(&model.CommentEntry{
		Base: model.Base{CreatedAt: latestAt}, TargetID: target.ID, AuthorID: user.UUID,
		Content: "latest reply", ContentHash: "latest-reply", Status: "active",
	}).Error; err != nil {
		t.Fatalf("create comment: %v", err)
	}
	if err := db.Create(&model.ForumLike{UserID: user.UUID, TargetType: "topic", TargetID: topic.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&model.ForumBookmark{UserID: user.UUID, TopicID: topic.ID}).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}

	type stateTopic struct {
		model.ForumTopic
	}
	for _, path := range []string{
		"/api/v1/forum/topics",
		"/api/v1/forum/search?q=needle",
		"/api/v1/forum/topics/" + topic.ID.String(),
	} {
		response := performForumRequest(t, router, http.MethodGet, path, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("expected 200 from %s, got %d: %s", path, response.Code, response.Body.String())
		}
		if path == "/api/v1/forum/topics/"+topic.ID.String() {
			item, _ := decodeForumData[stateTopic](t, response)
			if item.ReplyCount != 1 || item.LastReplyAt == nil || !item.IsLiked || !item.IsBookmarked {
				t.Fatalf("unexpected detail state: %s", response.Body.String())
			}
			continue
		}
		items, _ := decodeForumData[[]stateTopic](t, response)
		if len(items) != 1 || items[0].ReplyCount != 1 || items[0].LastReplyAt == nil || !items[0].IsLiked || !items[0].IsBookmarked {
			t.Fatalf("unexpected list/search state from %s: %s", path, response.Body.String())
		}
	}

	service := NewService(db)
	anonymousTopics, _, err := service.ListTopics(authctx.CurrentUser{Role: authctx.RoleAnonymous}, ListTopicsQuery{})
	if err != nil {
		t.Fatalf("list anonymous topics: %v", err)
	}
	if len(anonymousTopics) != 1 || anonymousTopics[0].IsLiked || anonymousTopics[0].IsBookmarked {
		t.Fatalf("anonymous state leaked: %#v", anonymousTopics)
	}
}

func TestTopicDTOReturnsTopicCapabilitiesForOwnerAndModeratorAssignment(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	_ = router
	topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "Capabilities", Content: "Body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	service := NewService(db)
	ownerDTO, err := service.topicDTO(db, topic, authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleUser})
	if err != nil {
		t.Fatalf("load owner topic dto: %v", err)
	}
	if !ownerDTO.CanEditTopic || ownerDTO.CanPinTopic || ownerDTO.CanLockTopic ||
		!ownerDTO.Permissions.Edit || !ownerDTO.Permissions.Delete || !ownerDTO.Permissions.Reply ||
		!ownerDTO.Permissions.MarkAnswer || ownerDTO.Permissions.Pin || ownerDTO.Permissions.Lock ||
		ownerDTO.Permissions.Feature || !ownerDTO.Permissions.Report ||
		ownerDTO.State.Closed || ownerDTO.State.Solved || ownerDTO.State.Pinned || ownerDTO.State.Featured {
		t.Fatalf("unexpected owner capabilities: %#v", ownerDTO)
	}
	moderator := model.User{Username: "forum-moderator", Email: "forum-moderator@example.com", Password: "hash", Role: authctx.RoleModerator, IsActive: true}
	if err := db.Create(&moderator).Error; err != nil {
		t.Fatalf("create moderator: %v", err)
	}
	if err := db.Create(&model.ForumModeratorAssignment{
		UserID: moderator.UUID, CategoryID: &category.ID, CanPinTopic: true, CanLockTopic: false,
	}).Error; err != nil {
		t.Fatalf("create moderator assignment: %v", err)
	}
	moderatorDTO, err := service.topicDTO(db, topic, authctx.CurrentUser{ID: moderator.UUID, Role: authctx.RoleModerator})
	if err != nil {
		t.Fatalf("load moderator topic dto: %v", err)
	}
	if !moderatorDTO.CanEditTopic || !moderatorDTO.CanPinTopic || moderatorDTO.CanLockTopic ||
		!moderatorDTO.Permissions.Edit || !moderatorDTO.Permissions.Delete || !moderatorDTO.Permissions.Reply ||
		moderatorDTO.Permissions.MarkAnswer || !moderatorDTO.Permissions.Pin || moderatorDTO.Permissions.Lock ||
		moderatorDTO.Permissions.Feature || !moderatorDTO.Permissions.Report {
		t.Fatalf("unexpected moderator capabilities: %#v", moderatorDTO)
	}
}

func TestUpdateTopicPreservesTagsWhenFieldIsOmitted(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	topic := model.ForumTopic{
		UserID: user.UUID, CategoryID: category.ID, Title: "Original", Content: "Original body",
		Tags: model.StringSlice{"go", "sqlite"},
	}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}

	response := performForumRequest(t, router, http.MethodPut, "/api/v1/forum/topics/"+topic.ID.String(), map[string]any{
		"title": "Updated", "content": "Updated body",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	updated, _ := decodeForumData[model.ForumTopic](t, response)
	if got := []string(updated.Tags); len(got) != 2 || got[0] != "go" || got[1] != "sqlite" {
		t.Fatalf("expected omitted tags to preserve existing value, got %#v", got)
	}

	response = performForumRequest(t, router, http.MethodPut, "/api/v1/forum/topics/"+topic.ID.String(), map[string]any{
		"title": "Updated", "content": "Updated body", "tags": []string{},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	updated, _ = decodeForumData[model.ForumTopic](t, response)
	if len(updated.Tags) != 0 {
		t.Fatalf("expected explicit empty tags to clear existing value, got %#v", updated.Tags)
	}
}

func TestCreateTopicRejectsInvalidTags(t *testing.T) {
	router, _, _, category := newForumHTTPTestRouter(t)
	tests := []struct {
		name string
		tags []string
	}{
		{name: "more than five", tags: []string{"1", "2", "3", "4", "5", "6"}},
		{name: "longer than thirty characters", tags: []string{"1234567890123456789012345678901"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/topics", map[string]any{
				"category_id": category.ID, "title": "Topic", "content": "Body", "tags": test.tags,
			})
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestListTopicsSupportsFiltersSortAndPagination(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	activeAt := base.Add(4 * time.Hour)
	topics := []model.ForumTopic{
		{Base: model.Base{CreatedAt: base}, UserID: user.UUID, CategoryID: category.ID, Title: "Go first", Content: "alpha", Tags: model.StringSlice{"go"}, LikeCount: 1},
		{Base: model.Base{CreatedAt: base.Add(time.Hour)}, UserID: user.UUID, CategoryID: category.ID, Title: "Go top", Content: "beta", Tags: model.StringSlice{"go", "db"}, LikeCount: 9, ReplyCount: 2},
		{Base: model.Base{CreatedAt: base.Add(2 * time.Hour)}, UserID: user.UUID, CategoryID: category.ID, Title: "Other", Content: "gamma", Tags: model.StringSlice{"rust"}, Featured: true, LastReplyAt: &activeAt},
	}
	for index := range topics {
		if err := db.Create(&topics[index]).Error; err != nil {
			t.Fatalf("create topic %d: %v", index, err)
		}
	}
	topTarget := model.DiscussionTarget{Kind: "forum_topic", ResourceID: topics[1].ID, ResourceKey: topics[1].ID.String(), CommentCount: 2, RootCount: 2}
	activeTarget := model.DiscussionTarget{Kind: "forum_topic", ResourceID: topics[2].ID, ResourceKey: topics[2].ID.String(), CommentCount: 1, RootCount: 1}
	if err := db.Create(&topTarget).Error; err != nil {
		t.Fatalf("create top discussion target: %v", err)
	}
	if err := db.Create(&activeTarget).Error; err != nil {
		t.Fatalf("create active discussion target: %v", err)
	}
	if err := db.Create(&model.CommentEntry{
		Base: model.Base{CreatedAt: activeAt}, TargetID: activeTarget.ID, AuthorID: user.UUID,
		Content: "latest", ContentHash: "latest", Status: "active", FloorNumber: new(int),
	}).Error; err != nil {
		t.Fatalf("create active comment: %v", err)
	}

	response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/topics?sort=top&tag=go&search=Go&page=1&page_size=1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	listed, envelope := decodeForumData[[]model.ForumTopic](t, response)
	if envelope.Meta.Total != 2 || envelope.Meta.PageSize != 1 || len(listed) != 1 || listed[0].Title != "Go top" {
		t.Fatalf("unexpected filtered page: data=%#v meta=%#v", listed, envelope.Meta)
	}

	response = performForumRequest(t, router, http.MethodGet, "/api/v1/forum/topics?sort=active&limit=2", nil)
	listed, envelope = decodeForumData[[]model.ForumTopic](t, response)
	if envelope.Meta.PageSize != 2 || len(listed) != 2 || listed[0].Title != "Other" {
		t.Fatalf("unexpected active/limit result: data=%#v meta=%#v", listed, envelope.Meta)
	}

	response = performForumRequest(t, router, http.MethodGet, "/api/v1/forum/topics?sort=featured", nil)
	listed, _ = decodeForumData[[]model.ForumTopic](t, response)
	if len(listed) != 3 || listed[0].Title != "Other" {
		t.Fatalf("unexpected featured result: %#v", listed)
	}
}

func TestListTopicsUsesStableIDTieBreakerAcrossPages(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	createdAt := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	ids := []uuid.UUID{
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		uuid.MustParse("00000000-0000-0000-0000-000000000004"),
		uuid.MustParse("00000000-0000-0000-0000-000000000005"),
		uuid.MustParse("00000000-0000-0000-0000-000000000006"),
	}
	for index, id := range ids {
		topic := model.ForumTopic{
			Base: model.Base{ID: id, CreatedAt: createdAt}, UserID: user.UUID, CategoryID: category.ID,
			Title: "same rank " + id.String(), Content: "body", LikeCount: 1, Featured: true,
		}
		if err := db.Create(&topic).Error; err != nil {
			t.Fatalf("create tied topic %d: %v", index, err)
		}
	}

	for _, sortName := range []string{"latest", "top", "active", "featured"} {
		t.Run(sortName, func(t *testing.T) {
			got := make([]uuid.UUID, 0, len(ids))
			seen := make(map[uuid.UUID]struct{}, len(ids))
			for page := 1; page <= 3; page++ {
				response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/topics?sort="+sortName+"&page="+strconv.Itoa(page)+"&page_size=2", nil)
				listed, envelope := decodeForumData[[]model.ForumTopic](t, response)
				if envelope.Meta.Total != int64(len(ids)) || envelope.Meta.Page != page || envelope.Meta.PageSize != 2 || len(listed) != 2 {
					t.Fatalf("unexpected page %d: data=%#v meta=%#v", page, listed, envelope.Meta)
				}
				for _, topic := range listed {
					if _, exists := seen[topic.ID]; exists {
						t.Fatalf("topic %s overlaps pages", topic.ID)
					}
					seen[topic.ID] = struct{}{}
					got = append(got, topic.ID)
				}
			}

			expected := []uuid.UUID{ids[5], ids[4], ids[3], ids[2], ids[1], ids[0]}
			if !slices.Equal(got, expected) {
				t.Fatalf("expected complete stable id-desc pages %v, got %v", expected, got)
			}
		})
	}
}

func TestDraftContextKeyReturnsAndDeletesSingleUserDraft(t *testing.T) {
	router, db, user, _ := newForumHTTPTestRouter(t)
	drafts := []model.ForumDraft{
		{UserID: user.UUID, ContextKey: "new_topic", Title: "new topic"},
		{UserID: user.UUID, ContextKey: "reply:topic-1", Content: "reply body"},
	}
	for index := range drafts {
		if err := db.Create(&drafts[index]).Error; err != nil {
			t.Fatalf("create draft %d: %v", index, err)
		}
	}

	response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/drafts?context_key=reply%3Atopic-1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	draft, _ := decodeForumData[*model.ForumDraft](t, response)
	if draft == nil || draft.ContextKey != "reply:topic-1" {
		t.Fatalf("expected matching draft, got %#v", draft)
	}

	response = performForumRequest(t, router, http.MethodGet, "/api/v1/forum/drafts?context_key=missing", nil)
	missing, _ := decodeForumData[*model.ForumDraft](t, response)
	if missing != nil {
		t.Fatalf("expected null draft, got %#v", missing)
	}

	response = performForumRequest(t, router, http.MethodDelete, "/api/v1/forum/drafts?context_key=reply%3Atopic-1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&model.ForumDraft{}).Where("user_id = ? AND context_key = ?", user.UUID, "reply:topic-1").Count(&count).Error; err != nil {
		t.Fatalf("count draft: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected context draft deleted, count=%d", count)
	}
}
