package forum

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newForumCommentCoreTestService(t *testing.T) (*Service, *comment.Service, *gorm.DB, authctx.CurrentUser, authctx.CurrentUser) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.ForumReply{},
		&model.MediaAsset{}, &model.DiscussionTarget{}, &model.CommentEntry{},
		&model.CommentMention{}, &model.CommentAttachment{}, &model.CommentLike{},
		&model.CommentReport{}, &model.CommentTimeAnchor{}, &model.CommentPublishRecord{},
		&model.Notification{}, &model.AuditLog{}, &model.TimelineRevisionProposal{},
		&model.DebateArgumentDetail{}, &model.DebateArgumentReference{}, &model.DebateArgumentDebateRef{},
	)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_discussion_target_kind_key ON discussion_targets (kind, resource_key)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_comment_root_floor ON comment_entries (target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_notification_dedup ON notifications (recipient_id, source_type, source_id) WHERE aggregation_key = '' AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_notification_unread_aggregate ON notifications (recipient_id, aggregation_key) WHERE aggregation_key <> '' AND read_at IS NULL AND deleted_at IS NULL`).Error)

	owner := createForumCoreUser(t, db, "owner")
	participant := createForumCoreUser(t, db, "participant")
	commentService := comment.NewService(db, comment.NewTargetRegistry(db))
	return NewService(db, commentService), commentService, db, owner, participant
}

func createForumCoreUser(t *testing.T, db *gorm.DB, username string) authctx.CurrentUser {
	t.Helper()
	user := model.User{Username: username, Email: username + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	return authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
}

func TestCreateReplyWritesCommentCoreAndMapsQuotedReply(t *testing.T) {
	service, _, db, owner, participant := newForumCommentCoreTestService(t)
	topic := createForumTestTopic(t, db, owner)

	root, err := service.CreateReply(participant, CreateReplyRequest{TopicID: topic.ID, Content: "first reply"})
	require.NoError(t, err)
	require.NotNil(t, root.FloorNumber)
	require.Equal(t, 1, *root.FloorNumber)

	child, err := service.CreateReply(owner, CreateReplyRequest{TopicID: topic.ID, ParentReplyID: &root.ID, Content: "quoted reply"})
	require.NoError(t, err)
	require.Equal(t, &root.ID, child.RootID)
	require.Equal(t, &root.ID, child.ReplyToID)
	require.Nil(t, child.FloorNumber)

	var legacyCount int64
	require.NoError(t, db.Model(&model.ForumReply{}).Count(&legacyCount).Error)
	require.Zero(t, legacyCount)
}

func TestGetTopicAggregatesReplyStateFromCommentCore(t *testing.T) {
	service, commentService, db, owner, participant := newForumCommentCoreTestService(t)
	topic := createForumTestTopic(t, db, owner)
	root, err := service.CreateReply(participant, CreateReplyRequest{TopicID: topic.ID, Content: "answer"})
	require.NoError(t, err)
	child, err := service.CreateReply(owner, CreateReplyRequest{TopicID: topic.ID, ParentReplyID: &root.ID, Content: "follow-up"})
	require.NoError(t, err)
	require.NoError(t, commentService.Mark(owner, comment.TargetRef{Kind: comment.TargetKindForumTopic, ResourceID: topic.ID}, root.ID))

	loaded, err := service.GetTopic(topic.ID)
	require.NoError(t, err)
	require.Equal(t, 2, loaded.ReplyCount)
	require.True(t, loaded.IsSolved)
	require.Equal(t, &root.ID, loaded.SolvedReplyID)
	require.NotNil(t, loaded.LastReplyAt)
	require.WithinDuration(t, child.CreatedAt, *loaded.LastReplyAt, time.Second)
}
