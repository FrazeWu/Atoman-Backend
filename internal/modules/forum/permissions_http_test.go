package forum

import (
	"net/http"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func newPermissionHTTPRouter(t *testing.T, role string) (*gin.Engine, *gorm.DB, model.User, model.User, model.ForumCategory) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.ForumDraft{},
		&model.ForumGroup{}, &model.ForumGroupMember{}, &model.ForumCategoryPermission{},
		&model.ForumUserModerationAction{},
		&model.ForumLike{}, &model.ForumUserTrust{},
		&model.DiscussionTarget{}, &model.CommentEntry{},
	)
	actor := model.User{Username: "actor-" + role, Email: "actor-" + role + "@example.com", Password: "hash", Role: role, IsActive: true}
	member := model.User{Username: "group-member", Email: "group-member-http@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	for _, user := range []*model.User{&actor, &member} {
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	category := model.ForumCategory{Name: "HTTP category", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: actor.UUID, Username: actor.Username, Role: actor.Role})
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1/forum"), NewService(db))
	return router, db, actor, member, category
}

func TestForumGroupAndCategoryPermissionHTTPCRUD(t *testing.T) {
	router, _, _, member, category := newPermissionHTTPRouter(t, authctx.RoleAdmin)

	response := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/groups", map[string]any{"name": "Members", "description": "Initial"})
	if response.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", response.Code, response.Body.String())
	}
	group, _ := decodeForumData[model.ForumGroup](t, response)

	response = performForumRequest(t, router, http.MethodPut, "/api/v1/forum/groups/"+group.ID.String(), map[string]any{"name": "Writers", "description": "Updated"})
	if response.Code != http.StatusOK {
		t.Fatalf("update group: %d %s", response.Code, response.Body.String())
	}
	updated, _ := decodeForumData[model.ForumGroup](t, response)
	if updated.Name != "Writers" || updated.Description != "Updated" {
		t.Fatalf("unexpected updated group: %#v", updated)
	}

	response = performForumRequest(t, router, http.MethodPut, "/api/v1/forum/groups/"+group.ID.String()+"/members/"+member.UUID.String(), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("add member: %d %s", response.Code, response.Body.String())
	}
	response = performForumRequest(t, router, http.MethodGet, "/api/v1/forum/groups", nil)
	groups, _ := decodeForumData[[]model.ForumGroup](t, response)
	if len(groups) != 1 || len(groups[0].Members) != 1 || groups[0].Members[0].UserID != member.UUID {
		t.Fatalf("unexpected group members: %#v", groups)
	}

	response = performForumRequest(t, router, http.MethodPut, "/api/v1/forum/category-permissions", map[string]any{
		"category_id": category.ID, "group_id": group.ID, "can_view": true, "can_create_topic": true, "can_comment": true,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("put permission: %d %s", response.Code, response.Body.String())
	}
	permission, _ := decodeForumData[model.ForumCategoryPermission](t, response)

	response = performForumRequest(t, router, http.MethodGet, "/api/v1/forum/category-permissions?category_id="+category.ID.String(), nil)
	permissions, _ := decodeForumData[[]model.ForumCategoryPermission](t, response)
	if len(permissions) != 1 || !permissions[0].CanComment {
		t.Fatalf("unexpected permissions: %#v", permissions)
	}

	response = performForumRequest(t, router, http.MethodDelete, "/api/v1/forum/category-permissions/"+permission.ID.String(), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("delete permission: %d %s", response.Code, response.Body.String())
	}
	response = performForumRequest(t, router, http.MethodDelete, "/api/v1/forum/groups/"+group.ID.String()+"/members/"+member.UUID.String(), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("remove member: %d %s", response.Code, response.Body.String())
	}
	response = performForumRequest(t, router, http.MethodDelete, "/api/v1/forum/groups/"+group.ID.String(), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("delete group: %d %s", response.Code, response.Body.String())
	}
	response = performForumRequest(t, router, http.MethodPost, "/api/v1/forum/groups", map[string]any{"name": "Writers"})
	if response.Code != http.StatusCreated {
		t.Fatalf("recreate deleted group: %d %s", response.Code, response.Body.String())
	}
}

func TestCategoryPermissionHTTPRejectsCreateWithoutView(t *testing.T) {
	router, db, _, _, category := newPermissionHTTPRouter(t, authctx.RoleAdmin)
	group := model.ForumGroup{Name: "Invalid"}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	response := performForumRequest(t, router, http.MethodPut, "/api/v1/forum/category-permissions", map[string]any{
		"category_id": category.ID, "group_id": group.ID, "can_create_topic": true,
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
}

func TestForumGroupHTTPRequiresAdmin(t *testing.T) {
	router, _, _, _, _ := newPermissionHTTPRouter(t, authctx.RoleUser)
	response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/groups", nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", response.Code, response.Body.String())
	}
}

func TestCreateForumGroupHTTPMapsDuplicateNameToConflict(t *testing.T) {
	router, _, _, _, _ := newPermissionHTTPRouter(t, authctx.RoleAdmin)
	response := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/groups", map[string]any{"name": "Duplicate"})
	if response.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", response.Code, response.Body.String())
	}
	response = performForumRequest(t, router, http.MethodPost, "/api/v1/forum/groups", map[string]any{"name": "Duplicate"})
	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", response.Code, response.Body.String())
	}
}
