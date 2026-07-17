package forum

import (
	"errors"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newPermissionTestService(t *testing.T) (*Service, authctx.CurrentUser, authctx.CurrentUser, authctx.CurrentUser, model.ForumCategory) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumFollow{},
		&model.ForumGroup{},
		&model.ForumGroupMember{},
		&model.ForumCategoryPermission{},
		&model.ForumUserModerationAction{},
		&model.ForumLike{},
		&model.ForumUserTrust{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentLike{},
	)
	users := []model.User{
		{Username: "admin", Email: "permission-admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true},
		{Username: "member", Email: "permission-member@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true},
		{Username: "outsider", Email: "permission-outsider@example.com", Password: "hash", Role: authctx.RoleModerator, IsActive: true},
	}
	for index := range users {
		if err := db.Create(&users[index]).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	category := model.ForumCategory{Name: "Private", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	current := func(user model.User) authctx.CurrentUser {
		return authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
	}
	return NewService(db), current(users[0]), current(users[1]), current(users[2]), category
}

func requireAppCode(t *testing.T, err error, code string) {
	t.Helper()
	var app *apperr.AppError
	if !errors.As(err, &app) || app.Code != code {
		t.Fatalf("expected app code %s, got %v", code, err)
	}
}

func TestForumCommentPolicyEnforcesCategoryVisibility(t *testing.T) {
	svc, admin, member, outsider, category := newPermissionTestService(t)
	group, err := svc.CreateGroup(admin, UpsertForumGroupRequest{Name: "Readers"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddGroupMember(admin, group.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutCategoryPermission(admin, PutCategoryPermissionRequest{CategoryID: category.ID, GroupID: group.ID, CanView: true}); err != nil {
		t.Fatal(err)
	}
	topic := model.ForumTopic{UserID: admin.ID, CategoryID: category.ID, Title: "Private", Content: "Body"}
	if err := svc.db.Create(&topic).Error; err != nil {
		t.Fatal(err)
	}

	if err := svc.CanViewTopic(comment.Viewer{Role: authctx.RoleAnonymous}, topic.ID); err == nil {
		t.Fatal("expected anonymous viewer to be denied")
	}
	if err := svc.CanViewTopic(comment.Viewer{UserID: &outsider.ID, Role: outsider.Role}, topic.ID); err == nil {
		t.Fatal("expected non-member viewer to be denied")
	}
	if err := svc.CanViewTopic(comment.Viewer{UserID: &member.ID, Role: member.Role}, topic.ID); err != nil {
		t.Fatalf("expected member access: %v", err)
	}
	if err := svc.CanViewTopic(comment.Viewer{UserID: &admin.ID, Role: admin.Role}, topic.ID); err != nil {
		t.Fatalf("expected admin access: %v", err)
	}
}

func TestForumCommentPolicyEnforcesCommentPermissionAndSilence(t *testing.T) {
	svc, admin, member, _, category := newPermissionTestService(t)
	group, err := svc.CreateGroup(admin, UpsertForumGroupRequest{Name: "Readers"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddGroupMember(admin, group.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutCategoryPermission(admin, PutCategoryPermissionRequest{CategoryID: category.ID, GroupID: group.ID, CanView: true}); err != nil {
		t.Fatal(err)
	}
	topic := model.ForumTopic{UserID: admin.ID, CategoryID: category.ID, Title: "Policy", Content: "Body"}
	if err := svc.db.Create(&topic).Error; err != nil {
		t.Fatal(err)
	}

	err = svc.db.Transaction(func(tx *gorm.DB) error {
		return svc.CheckCreateComment(tx, member, topic.ID, "first")
	})
	requireAppCode(t, err, "forum.category_permission_denied")

	if _, err := svc.PutCategoryPermission(admin, PutCategoryPermissionRequest{CategoryID: category.ID, GroupID: group.ID, CanView: true, CanComment: true}); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Hour)
	action := model.ForumUserModerationAction{UserID: member.ID, ActorID: admin.ID, Action: "silence", Reason: "test", ExpiresAt: &expires}
	if err := svc.db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	err = svc.db.Transaction(func(tx *gorm.DB) error {
		return svc.CheckCreateComment(tx, member, topic.ID, "second")
	})
	requireAppCode(t, err, "forum.user_silenced")
}

func TestForumCommentPolicyEnforcesCreateAndEditDuplicates(t *testing.T) {
	svc, admin, member, _, category := newPermissionTestService(t)
	topic := model.ForumTopic{UserID: admin.ID, CategoryID: category.ID, Title: "Duplicates", Content: "Body"}
	if err := svc.db.Create(&topic).Error; err != nil {
		t.Fatal(err)
	}
	target := model.DiscussionTarget{Kind: comment.TargetKindForumTopic, ResourceID: topic.ID, ResourceKey: topic.ID.String()}
	if err := svc.db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	first := model.CommentEntry{TargetID: target.ID, AuthorID: member.ID, Content: "same text", Status: comment.CommentStatusActive}
	second := model.CommentEntry{TargetID: target.ID, AuthorID: member.ID, Content: "other text", Status: comment.CommentStatusActive}
	if err := svc.db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}

	err := svc.db.Transaction(func(tx *gorm.DB) error {
		return svc.CheckCreateComment(tx, member, topic.ID, " SAME  TEXT ")
	})
	requireAppCode(t, err, "forum.duplicate_reply")
	err = svc.db.Transaction(func(tx *gorm.DB) error {
		return svc.CheckUpdateComment(tx, member.ID, second.ID, "same text")
	})
	requireAppCode(t, err, "forum.duplicate_reply")
}

func requireAppStatus(t *testing.T, err error, status int) {
	t.Helper()
	var app *apperr.AppError
	if !errors.As(err, &app) || app.HTTPStatus != status {
		t.Fatalf("expected app status %d, got %v", status, err)
	}
}

func TestCategoryPermissionsDefaultToPublicViewAndAuthenticatedCreate(t *testing.T) {
	svc, _, member, _, category := newPermissionTestService(t)

	if categories, err := svc.ListCategories(authctx.CurrentUser{Role: authctx.RoleAnonymous}); err != nil || len(categories) != 1 {
		t.Fatalf("expected public category, categories=%#v err=%v", categories, err)
	}
	if err := svc.CanCreateTopic(member, category.ID); err != nil {
		t.Fatalf("expected authenticated user to create by default: %v", err)
	}
	if err := svc.CanCreateTopic(authctx.CurrentUser{Role: authctx.RoleAnonymous}, category.ID); err == nil {
		t.Fatal("expected anonymous create to fail")
	}
}

func TestConfiguredCategoryRequiresMatchingGroupAndAdminBypasses(t *testing.T) {
	svc, admin, member, outsider, category := newPermissionTestService(t)
	group, err := svc.CreateGroup(admin, UpsertForumGroupRequest{Name: "Writers", Description: "Can publish"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := svc.AddGroupMember(admin, group.ID, member.ID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if _, err := svc.PutCategoryPermission(admin, PutCategoryPermissionRequest{
		CategoryID: category.ID, GroupID: group.ID, CanView: true, CanCreateTopic: true, CanComment: true,
	}); err != nil {
		t.Fatalf("put permission: %v", err)
	}

	if _, err := svc.GetCategory(member, category.ID); err != nil {
		t.Fatalf("expected member view: %v", err)
	}
	if _, err := svc.GetCategory(admin, category.ID); err != nil {
		t.Fatalf("expected admin bypass: %v", err)
	}
	if _, err := svc.GetCategory(outsider, category.ID); err == nil {
		t.Fatal("expected outsider category lookup to be hidden")
	}
	if err := svc.CanCreateTopic(member, category.ID); err != nil {
		t.Fatalf("expected member create: %v", err)
	}
	if err := svc.CanComment(member, category.ID); err != nil {
		t.Fatalf("expected member comment: %v", err)
	}
	if err := svc.CanCreateTopic(outsider, category.ID); err == nil {
		t.Fatal("expected outsider create to fail")
	}
}

func TestInvisibleCategoryDoesNotLeakThroughTopicListsOrLookup(t *testing.T) {
	svc, admin, member, outsider, category := newPermissionTestService(t)
	group, err := svc.CreateGroup(admin, UpsertForumGroupRequest{Name: "Readers"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := svc.AddGroupMember(admin, group.ID, member.ID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if _, err := svc.PutCategoryPermission(admin, PutCategoryPermissionRequest{CategoryID: category.ID, GroupID: group.ID, CanView: true}); err != nil {
		t.Fatalf("put permission: %v", err)
	}
	topic := model.ForumTopic{UserID: admin.ID, CategoryID: category.ID, Title: "Hidden", Content: "Body"}
	if err := svc.db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}

	listed, total, err := svc.ListTopics(outsider, ListTopicsQuery{})
	if err != nil || total != 0 || len(listed) != 0 {
		t.Fatalf("expected hidden list, topics=%#v total=%d err=%v", listed, total, err)
	}
	if _, err := svc.GetTopic(outsider, topic.ID); err == nil {
		t.Fatal("expected hidden topic lookup")
	}
	listed, total, err = svc.ListTopics(member, ListTopicsQuery{})
	if err != nil || total != 1 || len(listed) != 1 {
		t.Fatalf("expected member topic, topics=%#v total=%d err=%v", listed, total, err)
	}
}

func TestPutCategoryPermissionRejectsActionsWithoutView(t *testing.T) {
	svc, admin, _, _, category := newPermissionTestService(t)
	group, err := svc.CreateGroup(admin, UpsertForumGroupRequest{Name: "Invalid"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	_, err = svc.PutCategoryPermission(admin, PutCategoryPermissionRequest{
		CategoryID: category.ID, GroupID: group.ID, CanCreateTopic: true,
	})
	if err == nil {
		t.Fatal("expected invalid permission combination")
	}
}

func TestForumGroupManagementRequiresAdmin(t *testing.T) {
	svc, _, member, _, _ := newPermissionTestService(t)
	_, err := svc.CreateGroup(member, UpsertForumGroupRequest{Name: "Denied"})
	if err == nil {
		t.Fatal("expected non-admin to be denied")
	}
	requireAppStatus(t, err, 403)
}

func TestForumGroupMemberValidatesIDs(t *testing.T) {
	svc, admin, _, _, _ := newPermissionTestService(t)
	if err := svc.AddGroupMember(admin, uuid.Nil, uuid.New()); err == nil {
		t.Fatal("expected invalid group id")
	}
}

func TestAdminPermissionBypassStillRequiresExistingCategory(t *testing.T) {
	svc, admin, _, _, _ := newPermissionTestService(t)
	if err := svc.CanCreateTopic(admin, uuid.New()); err == nil {
		t.Fatal("expected missing category to fail")
	}
}

func TestFollowTagOnlyFindsTagsInVisibleCategories(t *testing.T) {
	svc, admin, member, outsider, category := newPermissionTestService(t)
	group, err := svc.CreateGroup(admin, UpsertForumGroupRequest{Name: "Tag readers"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := svc.AddGroupMember(admin, group.ID, member.ID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if _, err := svc.PutCategoryPermission(admin, PutCategoryPermissionRequest{CategoryID: category.ID, GroupID: group.ID, CanView: true}); err != nil {
		t.Fatalf("put permission: %v", err)
	}
	topic := model.ForumTopic{UserID: admin.ID, CategoryID: category.ID, Title: "Hidden tag", Content: "Body", Tags: model.StringSlice{"secret"}}
	if err := svc.db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}

	if _, err := svc.Follow(outsider, model.ForumFollowTargetTag, "secret"); err == nil {
		t.Fatal("expected hidden-only tag to be not found")
	} else {
		requireAppStatus(t, err, 404)
	}
	if _, err := svc.Follow(member, model.ForumFollowTargetTag, "secret"); err != nil {
		t.Fatalf("expected member to follow visible tag: %v", err)
	}
	if _, err := svc.Follow(admin, model.ForumFollowTargetTag, "secret"); err != nil {
		t.Fatalf("expected admin to follow tag: %v", err)
	}
}

func TestDuplicateForumGroupNameReturnsConflict(t *testing.T) {
	svc, admin, _, _, _ := newPermissionTestService(t)
	if _, err := svc.CreateGroup(admin, UpsertForumGroupRequest{Name: "Duplicate"}); err != nil {
		t.Fatalf("create first group: %v", err)
	}
	if _, err := svc.CreateGroup(admin, UpsertForumGroupRequest{Name: "Duplicate"}); err == nil {
		t.Fatal("expected duplicate group conflict")
	} else {
		requireAppStatus(t, err, 409)
	}
	other, err := svc.CreateGroup(admin, UpsertForumGroupRequest{Name: "Other"})
	if err != nil {
		t.Fatalf("create other group: %v", err)
	}
	if _, err := svc.UpdateGroup(admin, other.ID, UpsertForumGroupRequest{Name: "Duplicate"}); err == nil {
		t.Fatal("expected duplicate group update conflict")
	} else {
		requireAppStatus(t, err, 409)
	}
}
