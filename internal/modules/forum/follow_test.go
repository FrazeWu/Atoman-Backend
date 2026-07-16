package forum

import (
	"net/http"
	"net/url"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func newForumFollowTestService(t *testing.T) (*Service, authctx.CurrentUser, model.ForumCategory, model.ForumTopic) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumFollow{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
	)
	user := model.User{Username: "follower", Email: "follower@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	category := model.ForumCategory{Name: "General", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	topic := model.ForumTopic{
		UserID: user.UUID, CategoryID: category.ID, Title: "Follow targets", Content: "Body",
		Tags: model.StringSlice{"Go 语言"},
	}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	return NewService(db), authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}, category, topic
}

func TestForumFollowSupportsAllTargetsAndIsIdempotent(t *testing.T) {
	service, user, category, topic := newForumFollowTestService(t)
	targets := []struct{ targetType, targetKey string }{
		{"topic", topic.ID.String()},
		{"category", category.ID.String()},
		{"tag", "  Go 语言  "},
	}
	for _, target := range targets {
		first, err := service.Follow(user, target.targetType, target.targetKey)
		if err != nil {
			t.Fatalf("follow %s: %v", target.targetType, err)
		}
		second, err := service.Follow(user, target.targetType, target.targetKey)
		if err != nil {
			t.Fatalf("follow %s again: %v", target.targetType, err)
		}
		if first.ID != second.ID {
			t.Fatalf("expected idempotent %s follow, got %s and %s", target.targetType, first.ID, second.ID)
		}
	}

	follows, err := service.ListFollows(user)
	if err != nil {
		t.Fatalf("list follows: %v", err)
	}
	if len(follows) != 3 {
		t.Fatalf("expected 3 follows, got %#v", follows)
	}
	if follows[2].TargetKey != "Go 语言" {
		t.Fatalf("expected trimmed tag key, got %#v", follows[2])
	}

	if err := service.Unfollow(user, "topic", topic.ID.String()); err != nil {
		t.Fatalf("unfollow topic: %v", err)
	}
	if err := service.Unfollow(user, "topic", topic.ID.String()); err != nil {
		t.Fatalf("unfollow topic again: %v", err)
	}
	follows, err = service.ListFollows(user)
	if err != nil || len(follows) != 2 {
		t.Fatalf("expected 2 follows after idempotent delete, got %#v, %v", follows, err)
	}
	if _, err := service.Follow(user, "topic", topic.ID.String()); err != nil {
		t.Fatalf("follow topic after delete: %v", err)
	}
	follows, err = service.ListFollows(user)
	if err != nil || len(follows) != 3 {
		t.Fatalf("expected re-follow to restore topic follow, got %#v, %v", follows, err)
	}
}

func TestForumFollowRejectsInvalidAndMissingTargets(t *testing.T) {
	service, user, _, _ := newForumFollowTestService(t)
	tests := []struct{ targetType, targetKey string }{
		{"post", uuid.NewString()},
		{"topic", "not-a-uuid"},
		{"topic", uuid.NewString()},
		{"category", uuid.NewString()},
		{"tag", "   "},
		{"tag", "1234567890123456789012345678901"},
		{"tag", "missing"},
	}
	for _, test := range tests {
		if _, err := service.Follow(user, test.targetType, test.targetKey); err == nil {
			t.Fatalf("expected %s/%q to fail", test.targetType, test.targetKey)
		}
	}
}

func TestListForumFollowerIDsReturnsDistinctTargetFollowers(t *testing.T) {
	service, firstUser, _, topic := newForumFollowTestService(t)
	db := service.db
	second := model.User{Username: "second", Email: "second@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second user: %v", err)
	}
	secondUser := authctx.CurrentUser{ID: second.UUID, Username: second.Username, Role: second.Role}
	for _, user := range []authctx.CurrentUser{firstUser, secondUser} {
		if _, err := service.Follow(user, "topic", topic.ID.String()); err != nil {
			t.Fatalf("follow topic: %v", err)
		}
	}
	ids, err := service.ListFollowerIDs("topic", topic.ID.String())
	if err != nil {
		t.Fatalf("list follower ids: %v", err)
	}
	if len(ids) != 2 || ids[0] != firstUser.ID || ids[1] != secondUser.ID {
		t.Fatalf("unexpected follower ids: %#v", ids)
	}
}

func TestForumFollowHTTPListsCreatesAndDeletesDecodedTarget(t *testing.T) {
	router, db, user, _ := newForumHTTPTestRouter(t)
	if err := db.AutoMigrate(&model.ForumFollow{}); err != nil {
		t.Fatalf("migrate follows: %v", err)
	}
	topic := model.ForumTopic{UserID: user.UUID, CategoryID: uuid.New(), Title: "Tagged", Content: "Body", Tags: model.StringSlice{"Go 语言"}}
	var category model.ForumCategory
	if err := db.First(&category).Error; err != nil {
		t.Fatalf("get category: %v", err)
	}
	topic.CategoryID = category.ID
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}

	paths := []string{
		"/api/v1/forum/follows/topic/" + topic.ID.String(),
		"/api/v1/forum/follows/category/" + category.ID.String(),
		"/api/v1/forum/follows/tag/" + url.PathEscape("Go 语言"),
	}
	for _, path := range paths {
		for range 2 {
			response := performForumRequest(t, router, http.MethodPut, path, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d: %s", path, response.Code, response.Body.String())
			}
		}
	}
	response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/follows", nil)
	follows, _ := decodeForumData[[]model.ForumFollow](t, response)
	if len(follows) != 3 || follows[2].TargetKey != "Go 语言" {
		t.Fatalf("unexpected follows: %#v", follows)
	}
	response = performForumRequest(t, router, http.MethodDelete, paths[2], nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected delete 200, got %d: %s", response.Code, response.Body.String())
	}
	response = performForumRequest(t, router, http.MethodPut, "/api/v1/forum/follows/topic/missing", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid topic 400, got %d: %s", response.Code, response.Body.String())
	}
}

func TestForumFollowHTTPSupportsSlashInTagViaQueryParameter(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	if err := db.AutoMigrate(&model.ForumFollow{}); err != nil {
		t.Fatalf("migrate follows: %v", err)
	}
	topic := model.ForumTopic{
		UserID: user.UUID, CategoryID: category.ID, Title: "Slash tag", Content: "Body",
		Tags: model.StringSlice{"go/rust"},
	}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}

	path := "/api/v1/forum/follows/tag?target_key=" + url.QueryEscape("go/rust")
	response := performForumRequest(t, router, http.MethodPut, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected follow 200, got %d: %s", response.Code, response.Body.String())
	}
	follow, _ := decodeForumData[model.ForumFollow](t, response)
	if follow.TargetKey != "go/rust" {
		t.Fatalf("expected decoded slash tag, got %#v", follow)
	}

	response = performForumRequest(t, router, http.MethodDelete, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected unfollow 200, got %d: %s", response.Code, response.Body.String())
	}
	follows, err := NewService(db).ListFollows(authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role})
	if err != nil || len(follows) != 0 {
		t.Fatalf("expected slash tag follow deleted, got %#v, %v", follows, err)
	}
}
