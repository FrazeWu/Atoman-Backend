package forum_moderation

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type forumModerationCommentContext struct {
	service     *Service
	comments    *comment.Service
	db          *gorm.DB
	owner       authctx.CurrentUser
	participant authctx.CurrentUser
	moderator   authctx.CurrentUser
	topic       model.ForumTopic
}

func newForumModerationTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.ForumCategory{}, &model.ForumModeratorAssignment{})
	admin := createModerationUser(t, db, "admin", authctx.RoleAdmin)
	return NewService(db), db, admin
}

func newForumModerationCommentContext(t *testing.T) forumModerationCommentContext {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.ForumReply{},
		&model.ForumModeratorAssignment{}, &model.MediaAsset{}, &model.DiscussionTarget{},
		&model.CommentEntry{}, &model.CommentMention{}, &model.CommentAttachment{},
		&model.CommentLike{}, &model.CommentReport{}, &model.CommentTimeAnchor{},
		&model.CommentPublishRecord{}, &model.Notification{}, &model.AuditLog{},
		&model.TimelineRevisionProposal{}, &model.DebateArgumentDetail{},
		&model.DebateArgumentReference{}, &model.DebateArgumentDebateRef{},
	)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_discussion_target_kind_key ON discussion_targets (kind, resource_key)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_comment_root_floor ON comment_entries (target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_notification_dedup ON notifications (recipient_id, source_type, source_id) WHERE aggregation_key = '' AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_notification_unread_aggregate ON notifications (recipient_id, aggregation_key) WHERE aggregation_key <> '' AND read_at IS NULL AND deleted_at IS NULL`).Error)

	owner := createModerationUser(t, db, "owner", authctx.RoleUser)
	participant := createModerationUser(t, db, "participant", authctx.RoleUser)
	moderator := createModerationUser(t, db, "moderator", authctx.RoleModerator)
	category := model.ForumCategory{Name: "General", Color: "#111111"}
	require.NoError(t, db.Create(&category).Error)
	topic := model.ForumTopic{UserID: owner.ID, CategoryID: category.ID, Title: "Topic", Content: "Body"}
	require.NoError(t, db.Create(&topic).Error)
	require.NoError(t, db.Create(&model.ForumModeratorAssignment{UserID: moderator.ID, CategoryID: &category.ID, CanLockTopic: true}).Error)
	comments := comment.NewService(db, comment.NewTargetRegistry(db))
	return forumModerationCommentContext{NewService(db, comments), comments, db, owner, participant, moderator, topic}
}

func createModerationUser(t *testing.T, db *gorm.DB, username, role string) authctx.CurrentUser {
	t.Helper()
	user := model.User{Username: username, Email: username + "@example.com", Password: "hash", Role: role, IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	return authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
}

func (ctx forumModerationCommentContext) createReply(t *testing.T, user authctx.CurrentUser, content string) comment.CommentDTO {
	t.Helper()
	reply, err := ctx.comments.Create(user, comment.TargetRef{Kind: comment.TargetKindForumTopic, ResourceID: ctx.topic.ID}, comment.CreateCommentInput{Content: content})
	require.NoError(t, err)
	return reply
}

func TestHideAndRestoreReplyModeratesCommentCore(t *testing.T) {
	ctx := newForumModerationCommentContext(t)
	reply := ctx.createReply(t, ctx.participant, "reply")

	_, err := ctx.service.HideReply(ctx.moderator, reply.ID)
	require.NoError(t, err)
	var stored model.CommentEntry
	require.NoError(t, ctx.db.First(&stored, "id = ?", reply.ID).Error)
	require.Equal(t, comment.CommentStatusModeratorHidden, stored.Status)

	_, err = ctx.service.RestoreReply(ctx.moderator, reply.ID)
	require.NoError(t, err)
	require.NoError(t, ctx.db.First(&stored, "id = ?", reply.ID).Error)
	require.Equal(t, comment.CommentStatusActive, stored.Status)

	var legacyCount int64
	require.NoError(t, ctx.db.Model(&model.ForumReply{}).Count(&legacyCount).Error)
	require.Zero(t, legacyCount)
}

func TestOnlyTopicOwnerCanSetOneBestAnswer(t *testing.T) {
	ctx := newForumModerationCommentContext(t)
	first := ctx.createReply(t, ctx.participant, "first")
	second := ctx.createReply(t, ctx.moderator, "second")

	_, err := ctx.service.SolveReply(ctx.participant, first.ID)
	require.ErrorIs(t, err, comment.ErrCommentForbidden)
	_, err = ctx.service.SolveReply(ctx.owner, first.ID)
	require.NoError(t, err)
	_, err = ctx.service.SolveReply(ctx.owner, second.ID)
	require.NoError(t, err)

	var target model.DiscussionTarget
	require.NoError(t, ctx.db.Where("kind = ? AND resource_id = ?", comment.TargetKindForumTopic, ctx.topic.ID).First(&target).Error)
	require.Equal(t, &second.ID, target.PinnedCommentID)
}
