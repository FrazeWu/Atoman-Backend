package forum

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newForumCommentCoreTestService(t *testing.T) (*Service, *comment.Service, *gorm.DB, authctx.CurrentUser, authctx.CurrentUser) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Post{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.ForumReply{},
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

func TestLegacyReplyMutationsRejectNonForumComment(t *testing.T) {
	service, commentService, db, owner, _ := newForumCommentCoreTestService(t)
	post := model.Post{UserID: owner.ID, Title: "Post", Content: "Body", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&post).Error)
	created, err := commentService.Create(owner, comment.TargetRef{Kind: comment.TargetKindBlogPost, ResourceID: post.ID}, comment.CreateCommentInput{Content: "blog comment"})
	require.NoError(t, err)

	_, err = service.UpdateReply(owner, created.ID, UpdateReplyRequest{Content: "forum overwrite"})
	require.Error(t, err)
	require.Error(t, service.DeleteReply(owner, created.ID))

	var stored model.CommentEntry
	require.NoError(t, db.First(&stored, "id = ?", created.ID).Error)
	require.Equal(t, "blog comment", stored.Content)
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

func TestLockedTopicRejectsNewRepliesButKeepsExistingReadable(t *testing.T) {
	service, _, db, owner, participant := newForumCommentCoreTestService(t)
	topic := createForumTestTopic(t, db, owner)
	root, err := service.CreateReply(participant, CreateReplyRequest{TopicID: topic.ID, Content: "existing"})
	require.NoError(t, err)
	require.NoError(t, db.Model(&topic).Update("closed", true).Error)

	_, err = service.CreateReply(owner, CreateReplyRequest{TopicID: topic.ID, Content: "blocked"})
	require.ErrorIs(t, err, comment.ErrTargetLocked)
	listed, err := service.ListReplies(topic.ID)
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	require.Equal(t, root.ID, listed.Items[0].ID)
}

func TestTopicLatestReplyIgnoresChildrenOfHiddenRoots(t *testing.T) {
	service, _, db, owner, participant := newForumCommentCoreTestService(t)
	topic := createForumTestTopic(t, db, owner)
	visible, err := service.CreateReply(participant, CreateReplyRequest{TopicID: topic.ID, Content: "visible"})
	require.NoError(t, err)
	hidden, err := service.CreateReply(participant, CreateReplyRequest{TopicID: topic.ID, Content: "hidden root"})
	require.NoError(t, err)
	child, err := service.CreateReply(owner, CreateReplyRequest{TopicID: topic.ID, ParentReplyID: &hidden.ID, Content: "hidden child"})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.CommentEntry{}).Where("id = ?", visible.ID).Update("created_at", time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)).Error)
	require.NoError(t, db.Model(&model.CommentEntry{}).Where("id = ?", child.ID).Update("created_at", time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)).Error)
	require.NoError(t, db.Model(&model.CommentEntry{}).Where("id = ?", hidden.ID).Update("status", comment.CommentStatusModeratorHidden).Error)

	loaded, err := service.GetTopic(topic.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.LastReplyAt)
	require.Equal(t, time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC), loaded.LastReplyAt.UTC())
}

func TestListTopicsSelectsOneLatestReplyPerTarget(t *testing.T) {
	service, _, db, owner, participant := newForumCommentCoreTestService(t)
	first := createForumTestTopic(t, db, owner)
	second := model.ForumTopic{UserID: owner.ID, CategoryID: first.CategoryID, Title: "Second", Content: "Body"}
	require.NoError(t, db.Create(&second).Error)
	firstReply, err := service.CreateReply(participant, CreateReplyRequest{TopicID: first.ID, Content: "first"})
	require.NoError(t, err)
	secondReply, err := service.CreateReply(participant, CreateReplyRequest{TopicID: second.ID, Content: "second"})
	require.NoError(t, err)
	timestamp := time.Date(2026, 2, 1, 2, 0, 0, 0, time.UTC)
	require.NoError(t, db.Model(&model.CommentEntry{}).Where("id IN ?", []uuid.UUID{firstReply.ID, secondReply.ID}).Update("created_at", timestamp).Error)

	topics, _, err := service.ListTopics(ListTopicsQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, topics, 2)
	for _, topic := range topics {
		require.NotNil(t, topic.LastReplyAt)
		require.Equal(t, timestamp, topic.LastReplyAt.UTC())
	}
}
