package debate

import (
	"errors"
	"testing"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seededDebateCommentService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser, model.Debate) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.MediaAsset{}, &model.Debate{}, &model.Argument{}, &model.DiscussionTarget{}, &model.CommentEntry{},
		&model.CommentMention{}, &model.CommentAttachment{}, &model.CommentTimeAnchor{},
		&model.CommentLike{}, &model.CommentReport{}, &model.CommentPublishRecord{}, &model.Notification{}, &model.AuditLog{}, &model.TimelineRevisionProposal{}, &model.DebateArgumentDetail{},
		&model.DebateArgumentReference{}, &model.DebateArgumentDebateRef{}, &model.DebateVote{}, &model.VoteHistory{},
	)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_discussion_target_kind_key ON discussion_targets (kind, resource_key)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_comment_root_floor ON comment_entries (target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_notification_dedup ON notifications (recipient_id, source_type, source_id) WHERE aggregation_key = '' AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_notification_unread_aggregate ON notifications (recipient_id, aggregation_key) WHERE aggregation_key <> '' AND read_at IS NULL AND deleted_at IS NULL`).Error)
	owner := model.User{Username: "owner", Email: "owner@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	user := authctx.CurrentUser{ID: owner.UUID, Username: owner.Username, Role: owner.Role}
	debate := model.Debate{UserID: owner.UUID, Title: "Typed debate", Status: "open"}
	require.NoError(t, db.Create(&debate).Error)
	comments := comment.NewService(db, comment.NewTargetRegistry(db))
	return NewService(db, comments), db, user, debate
}

func TestCreateArgumentWritesCommentAndTypedDetail(t *testing.T) {
	svc, db, user, debate := seededDebateCommentService(t)
	mentioned := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&mentioned).Error)
	got, err := svc.CreateArgument(user, CreateArgumentRequest{
		DebateID: debate.ID, Content: "@bob evidence", ArgumentType: "evidence", SourceURL: "https://example.com",
		Mentions: []comment.MentionInput{{UserID: mentioned.UUID, Start: 0, End: 4}},
	})
	require.NoError(t, err)

	var entry model.CommentEntry
	require.NoError(t, db.First(&entry, "id = ?", got.ID).Error)
	var detail model.DebateArgumentDetail
	require.NoError(t, db.First(&detail, "comment_id = ?", got.ID).Error)
	require.Equal(t, "evidence", detail.ArgumentType)
	require.Equal(t, got.ID, detail.CommentID)
	var mention model.CommentMention
	require.NoError(t, db.First(&mention, "comment_id = ?", got.ID).Error)
	require.Equal(t, mentioned.UUID, mention.UserID)
	var legacyCount int64
	require.NoError(t, db.Model(&model.Argument{}).Count(&legacyCount).Error)
	require.Zero(t, legacyCount)
}

func TestListArgumentsExcludesModerationHiddenComments(t *testing.T) {
	svc, db, user, debate := seededDebateCommentService(t)
	created, err := svc.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "claim", ArgumentType: "support"})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.CommentEntry{}).Where("id = ?", created.ID).Update("status", "hidden").Error)
	arguments, err := svc.ListArguments(debate.ID)
	require.NoError(t, err)
	require.Empty(t, arguments)
}

func TestCreateArgumentRollsBackCommentWhenDetailFails(t *testing.T) {
	svc, db, user, debate := seededDebateCommentService(t)
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("reject_argument_detail", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "debate_argument_details" {
			tx.AddError(errors.New("detail failed"))
		}
	}))
	_, err := svc.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "claim", ArgumentType: "support"})
	require.ErrorContains(t, err, "detail failed")
	var count int64
	require.NoError(t, db.Model(&model.CommentEntry{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestArgumentRepliesReferencesAndFoldingUseCommentIDs(t *testing.T) {
	svc, db, user, debate := seededDebateCommentService(t)
	root, err := svc.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "root", ArgumentType: "support"})
	require.NoError(t, err)
	child, err := svc.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, ParentID: &root.ID, Content: "child", ArgumentType: "counter"})
	require.NoError(t, err)
	grandchild, err := svc.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, ParentID: &child.ID, Content: "nested reply", ArgumentType: "question"})
	require.NoError(t, err)
	var childEntry, grandchildEntry model.CommentEntry
	require.NoError(t, db.First(&childEntry, "id = ?", child.ID).Error)
	require.NoError(t, db.First(&grandchildEntry, "id = ?", grandchild.ID).Error)
	require.Equal(t, root.ID, *childEntry.RootID)
	require.Equal(t, root.ID, *grandchildEntry.RootID)
	require.NoError(t, svc.AddArgumentReference(user, child.ID, root.ID))
	var relation model.DebateArgumentReference
	require.NoError(t, db.First(&relation, "comment_id = ? AND referenced_comment_id = ?", child.ID, root.ID).Error)

	admin := user
	admin.Role = authctx.RoleAdmin
	require.NoError(t, svc.FoldArgument(admin, root.ID, "duplicate"))
	loaded, err := svc.GetArgument(root.ID)
	require.NoError(t, err)
	require.True(t, loaded.IsFolded)
	require.Equal(t, "duplicate", loaded.FoldNote)
}

func TestCreateArgumentRejectsConcludedDebate(t *testing.T) {
	svc, db, user, debate := seededDebateCommentService(t)
	require.NoError(t, db.Model(&debate).Update("status", "concluded").Error)
	_, err := svc.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "late", ArgumentType: "support"})
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&model.CommentEntry{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestUpdateArgumentPreservesConclusion(t *testing.T) {
	svc, db, user, debate := seededDebateCommentService(t)
	created, err := svc.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "claim", ArgumentType: "support"})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.DebateArgumentDetail{}).Where("comment_id = ?", created.ID).Update("conclusion", "accepted").Error)
	updated, err := svc.UpdateArgument(user, created.ID, CreateArgumentRequest{Content: "updated", ArgumentType: "evidence"})
	require.NoError(t, err)
	require.Equal(t, "accepted", updated.Conclusion)
	require.NotEqual(t, uuid.Nil, updated.ID)
}

func TestDeleteArgumentRemovesTypedVotesAndReferences(t *testing.T) {
	svc, db, user, debate := seededDebateCommentService(t)
	root, err := svc.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "root", ArgumentType: "support"})
	require.NoError(t, err)
	child, err := svc.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, ParentID: &root.ID, Content: "child", ArgumentType: "counter"})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.DebateVote{ArgumentID: child.ID, UserID: user.ID, VoteType: 1}).Error)
	require.NoError(t, db.Create(&model.DebateArgumentReference{CommentID: child.ID, ReferencedCommentID: root.ID}).Error)
	require.NoError(t, svc.DeleteArgument(user, root.ID))
	var votes, references int64
	require.NoError(t, db.Model(&model.DebateVote{}).Where("argument_id IN ?", []uuid.UUID{root.ID, child.ID}).Count(&votes).Error)
	require.NoError(t, db.Model(&model.DebateArgumentReference{}).Where("comment_id IN ? OR referenced_comment_id IN ?", []uuid.UUID{root.ID, child.ID}, []uuid.UUID{root.ID, child.ID}).Count(&references).Error)
	require.Zero(t, votes)
	require.Zero(t, references)
}
