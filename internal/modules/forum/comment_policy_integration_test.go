package forum

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestUnifiedCommentsEnforceForumCategoryACLWithRealServices(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.MediaAsset{},
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
		&model.CommentMention{},
		&model.CommentAttachment{},
		&model.CommentLike{},
		&model.CommentTimeAnchor{},
		&model.CommentPublishRecord{},
		&model.Notification{},
	)
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_discussion_target_kind_key ON discussion_targets (kind, resource_key)`).Error; err != nil {
		t.Fatal(err)
	}

	users := []model.User{
		{Username: "acl-admin", Email: "acl-admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true},
		{Username: "acl-member", Email: "acl-member@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true},
		{Username: "acl-outsider", Email: "acl-outsider@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true},
	}
	for index := range users {
		if err := db.Create(&users[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	current := func(index int) authctx.CurrentUser {
		return authctx.CurrentUser{ID: users[index].UUID, Username: users[index].Username, Role: users[index].Role}
	}
	admin, member, outsider := current(0), current(1), current(2)
	category := model.ForumCategory{Name: "Restricted comments", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	forumService := NewService(db)
	group, err := forumService.CreateGroup(admin, UpsertForumGroupRequest{Name: "Comment readers"})
	if err != nil {
		t.Fatal(err)
	}
	if err := forumService.AddGroupMember(admin, group.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := forumService.PutCategoryPermission(admin, PutCategoryPermissionRequest{
		CategoryID: category.ID, GroupID: group.ID, CanView: true, CanComment: true,
	}); err != nil {
		t.Fatal(err)
	}
	topic := model.ForumTopic{UserID: admin.ID, CategoryID: category.ID, Title: "Restricted", Content: "Body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatal(err)
	}

	comments := comment.NewService(db, comment.NewTargetRegistry(db))
	comments.SetForumPolicy(forumService)
	target := comment.TargetRef{Kind: comment.TargetKindForumTopic, ResourceID: topic.ID}
	created, err := comments.Create(admin, target, comment.CreateCommentInput{Content: "restricted answer"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := comments.Get(comment.Viewer{UserID: &outsider.ID, Role: outsider.Role}, created.ID); err == nil {
		t.Fatal("expected outsider UUID lookup to be denied")
	}
	if err := comments.Like(outsider, created.ID); err == nil {
		t.Fatal("expected outsider UUID mutation to be denied")
	}
	if _, err := comments.Get(comment.Viewer{UserID: &admin.ID, Role: admin.Role}, created.ID); err != nil {
		t.Fatalf("expected administrator UUID lookup: %v", err)
	}
	if err := comments.Mark(admin, target, created.ID); err != nil {
		t.Fatalf("expected administrator mark: %v", err)
	}
	if err := comments.Unmark(admin, target); err != nil {
		t.Fatalf("expected administrator unmark: %v", err)
	}
}

func TestCommentNotificationAudienceFiltersFollowersByACLAndAccountState(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.ForumFollow{},
		&model.ForumGroup{}, &model.ForumGroupMember{}, &model.ForumCategoryPermission{},
	)
	users := []model.User{
		{Username: "audience-owner", Email: "audience-owner@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true},
		{Username: "audience-actor", Email: "audience-actor@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true},
		{Username: "audience-member", Email: "audience-member@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true},
		{Username: "audience-admin", Email: "audience-admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true},
		{Username: "audience-outsider", Email: "audience-outsider@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true},
		{Username: "audience-inactive", Email: "audience-inactive@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: false},
	}
	for index := range users {
		if err := db.Create(&users[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.User{}).Where("uuid = ?", users[5].UUID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	category := model.ForumCategory{Name: "Audience restricted", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	topic := model.ForumTopic{UserID: users[0].UUID, CategoryID: category.ID, Title: "Audience title", Content: "Body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatal(err)
	}
	group := model.ForumGroup{Name: "Audience members"}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ForumGroupMember{GroupID: group.ID, UserID: users[2].UUID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ForumGroupMember{GroupID: group.ID, UserID: users[5].UUID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ForumCategoryPermission{CategoryID: category.ID, GroupID: group.ID, CanView: true}).Error; err != nil {
		t.Fatal(err)
	}
	for _, user := range users[1:] {
		if err := db.Create(&model.ForumFollow{UserID: user.UUID, TargetType: model.ForumFollowTargetTopic, TargetKey: topic.ID.String()}).Error; err != nil {
			t.Fatal(err)
		}
	}

	title, ids, err := NewService(db).CommentNotificationAudience(db, topic.ID, users[1].UUID)
	if err != nil {
		t.Fatal(err)
	}
	if title != topic.Title {
		t.Fatalf("unexpected title: %q", title)
	}
	want := map[uuid.UUID]bool{users[0].UUID: true, users[2].UUID: true, users[3].UUID: true}
	if len(ids) != len(want) {
		t.Fatalf("unexpected audience: %#v", ids)
	}
	for _, id := range ids {
		if !want[id] {
			t.Fatalf("unexpected audience member: %s", id)
		}
	}
}
