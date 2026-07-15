package forum_engagement

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/stretchr/testify/require"
)

func TestToggleReplyLikeUsesCommentCore(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Post{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.ForumLike{},
		&model.MediaAsset{}, &model.DiscussionTarget{}, &model.CommentEntry{},
		&model.CommentMention{}, &model.CommentAttachment{}, &model.CommentLike{},
		&model.CommentReport{}, &model.CommentTimeAnchor{}, &model.CommentPublishRecord{},
		&model.Notification{}, &model.AuditLog{}, &model.TimelineRevisionProposal{},
		&model.DebateArgumentDetail{}, &model.DebateArgumentReference{}, &model.DebateArgumentDebateRef{},
	)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_discussion_target_kind_key ON discussion_targets (kind, resource_key)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_comment_root_floor ON comment_entries (target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_comment_like_user ON comment_likes (comment_id, user_id) WHERE deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_notification_dedup ON notifications (recipient_id, source_type, source_id) WHERE aggregation_key = '' AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_notification_unread_aggregate ON notifications (recipient_id, aggregation_key) WHERE aggregation_key <> '' AND read_at IS NULL AND deleted_at IS NULL`).Error)

	owner := model.User{Username: "owner", Email: "owner@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	liker := model.User{Username: "liker", Email: "liker@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&liker).Error)
	category := model.ForumCategory{Name: "General", Color: "#111111"}
	require.NoError(t, db.Create(&category).Error)
	topic := model.ForumTopic{UserID: owner.UUID, CategoryID: category.ID, Title: "Topic", Content: "Body"}
	require.NoError(t, db.Create(&topic).Error)
	commentService := comment.NewService(db, comment.NewTargetRegistry(db))
	reply, err := commentService.Create(authctx.CurrentUser{ID: owner.UUID, Username: owner.Username, Role: owner.Role}, comment.TargetRef{Kind: comment.TargetKindForumTopic, ResourceID: topic.ID}, comment.CreateCommentInput{Content: "reply"})
	require.NoError(t, err)
	service := NewService(db, commentService)
	current := authctx.CurrentUser{ID: liker.UUID, Username: liker.Username, Role: liker.Role}

	state, err := service.ToggleReplyLike(current, reply.ID)
	require.NoError(t, err)
	require.True(t, state.Liked)
	state, err = service.ToggleReplyLike(current, reply.ID)
	require.NoError(t, err)
	require.False(t, state.Liked)

	var coreLikes, legacyLikes int64
	require.NoError(t, db.Model(&model.CommentLike{}).Count(&coreLikes).Error)
	require.NoError(t, db.Model(&model.ForumLike{}).Where("target_type = ?", "reply").Count(&legacyLikes).Error)
	require.Zero(t, coreLikes)
	require.Zero(t, legacyLikes)
}

func TestToggleReplyLikeRejectsNonForumComment(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Post{}, &model.ForumLike{}, &model.MediaAsset{},
		&model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentMention{},
		&model.CommentAttachment{}, &model.CommentLike{}, &model.CommentReport{},
		&model.CommentTimeAnchor{}, &model.CommentPublishRecord{}, &model.Notification{},
		&model.AuditLog{}, &model.TimelineRevisionProposal{}, &model.DebateArgumentDetail{},
		&model.DebateArgumentReference{}, &model.DebateArgumentDebateRef{},
	)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_discussion_target_kind_key ON discussion_targets (kind, resource_key)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_comment_root_floor ON comment_entries (target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`).Error)
	user := model.User{Username: "blog-owner", Email: "blog-owner@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	post := model.Post{UserID: user.UUID, Title: "Post", Content: "Body", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&post).Error)
	current := authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
	comments := comment.NewService(db, comment.NewTargetRegistry(db))
	created, err := comments.Create(current, comment.TargetRef{Kind: comment.TargetKindBlogPost, ResourceID: post.ID}, comment.CreateCommentInput{Content: "blog comment"})
	require.NoError(t, err)

	_, err = NewService(db, comments).ToggleReplyLike(current, created.ID)
	require.Error(t, err)
	var likes int64
	require.NoError(t, db.Model(&model.CommentLike{}).Count(&likes).Error)
	require.Zero(t, likes)
}
