package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyDebate struct {
	model.Base
	UserID            uuid.UUID `gorm:"type:uuid;not null"`
	Title             string
	Description       string
	Content           string
	Status            string
	Tags              string
	ConclusionType    string
	ConclusionSummary string
	ArgumentCount     int
	VoteCount         int
	ConcludeVoteCount int
	ConcludeThreshold int
}

func (legacyDebate) TableName() string { return "debates" }

type legacyDebateVote struct {
	model.Base
	ArgumentID uuid.UUID
	UserID     uuid.UUID
	VoteType   int
}

func (legacyDebateVote) TableName() string { return "debate_votes" }

type legacyDebateRelation struct {
	model.Base
	SourceDebateID uuid.UUID
	TargetDebateID uuid.UUID
	Stance         string
	UserID         uuid.UUID
}

func (legacyDebateRelation) TableName() string { return "debate_relations" }

type legacyDebateArgumentDetail struct {
	CommentID    uuid.UUID `gorm:"primaryKey"`
	ArgumentType string
}

func (legacyDebateArgumentDetail) TableName() string { return "debate_argument_details" }

type legacyDebateArgumentReference struct {
	CommentID           uuid.UUID `gorm:"primaryKey"`
	ReferencedCommentID uuid.UUID `gorm:"primaryKey"`
}

func (legacyDebateArgumentReference) TableName() string { return "debate_argument_references" }

type legacyDebateArgumentDebateRef struct {
	CommentID uuid.UUID `gorm:"primaryKey"`
	DebateID  uuid.UUID `gorm:"primaryKey"`
}

func (legacyDebateArgumentDebateRef) TableName() string { return "debate_argument_debate_refs" }

type legacyVoteHistory struct {
	model.Base
	ArgumentID  uuid.UUID
	UserID      uuid.UUID
	NewVoteType int
}

func (legacyVoteHistory) TableName() string { return "vote_histories" }

type legacyDebateConcludeVote struct {
	model.Base
	DebateID uuid.UUID
	UserID   uuid.UUID
}

func (legacyDebateConcludeVote) TableName() string { return "debate_conclude_votes" }

func TestRunDebateWikiMigrationCleansLegacyArgumentsAndBackfillsRevision(t *testing.T) {
	db := testdb.Open(t)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &legacyDebate{}, &model.DiscussionTarget{}, &model.CommentEntry{},
		&model.CommentMention{}, &model.CommentAttachment{}, &model.CommentLike{}, &model.CommentReport{},
		&model.CommentTimeAnchor{}, &model.CommentPublishRecord{}, &model.TimelineRevisionProposal{}, &model.Notification{},
		&legacyDebateArgumentDetail{}, &legacyDebateArgumentReference{}, &legacyDebateArgumentDebateRef{},
		&legacyDebateVote{}, &legacyVoteHistory{}, &legacyDebateConcludeVote{}, &legacyDebateRelation{},
	))

	user := model.User{UUID: uuid.New(), Username: "legacy", Email: "legacy@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	debate := legacyDebate{UserID: user.UUID, Title: "Legacy", Description: "Summary", Content: "Body", Status: "concluded", Tags: `{tag}`, ConclusionType: "yes", ArgumentCount: 1, VoteCount: 1, ConcludeVoteCount: 1, ConcludeThreshold: 10}
	require.NoError(t, db.Create(&debate).Error)
	target := model.DiscussionTarget{
		Kind: "debate", ResourceID: debate.ID, ResourceKey: debate.ID.String(), OwnerID: &user.UUID,
		CommentCount: 99, RootCount: 77, NextFloor: 42,
	}
	require.NoError(t, db.Create(&target).Error)
	argumentFloor, ordinaryFloor := 1, 5
	argument := model.CommentEntry{TargetID: target.ID, AuthorID: user.UUID, FloorNumber: &argumentFloor, Content: "legacy argument", ContentHash: "legacy-root", Status: "active"}
	require.NoError(t, db.Create(&argument).Error)
	argumentReply := model.CommentEntry{TargetID: target.ID, AuthorID: user.UUID, RootID: &argument.ID, ReplyToID: &argument.ID, Content: "legacy argument reply", ContentHash: "legacy-reply", Status: "active"}
	require.NoError(t, db.Create(&argumentReply).Error)
	ordinary := model.CommentEntry{TargetID: target.ID, AuthorID: user.UUID, FloorNumber: &ordinaryFloor, Content: "ordinary discussion", ContentHash: "ordinary-root", Status: "active"}
	require.NoError(t, db.Create(&ordinary).Error)
	ordinaryReply := model.CommentEntry{TargetID: target.ID, AuthorID: user.UUID, RootID: &ordinary.ID, ReplyToID: &ordinary.ID, Content: "ordinary reply", ContentHash: "ordinary-reply", Status: "auto_folded"}
	require.NoError(t, db.Create(&ordinaryReply).Error)
	require.NoError(t, db.Model(&target).Update("pinned_comment_id", argument.ID).Error)

	for _, entry := range []model.CommentEntry{argument, argumentReply} {
		require.NoError(t, db.Create(&legacyDebateArgumentDetail{CommentID: entry.ID, ArgumentType: "support"}).Error)
	}
	for _, entry := range []model.CommentEntry{argument, ordinary} {
		require.NoError(t, db.Create(&model.CommentMention{CommentID: entry.ID, UserID: user.UUID}).Error)
		require.NoError(t, db.Create(&model.CommentAttachment{CommentID: entry.ID, MediaAssetID: uuid.New()}).Error)
		require.NoError(t, db.Create(&model.CommentLike{CommentID: entry.ID, UserID: user.UUID}).Error)
		require.NoError(t, db.Create(&model.CommentReport{CommentID: entry.ID, ReporterID: user.UUID, Reason: "spam"}).Error)
		require.NoError(t, db.Create(&model.CommentTimeAnchor{CommentID: entry.ID, Seconds: 10}).Error)
		require.NoError(t, db.Create(&model.TimelineRevisionProposal{CommentID: entry.ID, TargetKind: "debate", TargetID: debate.ID, PatchJSON: []byte(`{}`), Evidence: "source"}).Error)
		require.NoError(t, db.Create(&model.Notification{RecipientID: user.UUID, Type: "comment_reply", SourceType: "comment_event", SourceID: entry.ID}).Error)
	}
	require.NoError(t, db.Create(&model.Notification{RecipientID: user.UUID, Type: "debate_update", SourceType: "debate", SourceID: argument.ID}).Error)
	publishRecord := model.CommentPublishRecord{AuthorID: user.UUID, TargetID: target.ID, ContentHash: argument.ContentHash}
	require.NoError(t, db.Create(&publishRecord).Error)
	require.NoError(t, db.Create(&legacyDebateVote{ArgumentID: argument.ID, UserID: user.UUID, VoteType: 1}).Error)
	require.NoError(t, db.Create(&legacyVoteHistory{ArgumentID: argument.ID, UserID: user.UUID, NewVoteType: 1}).Error)
	require.NoError(t, db.Create(&legacyDebateConcludeVote{DebateID: debate.ID, UserID: user.UUID}).Error)
	require.NoError(t, db.Create(&legacyDebateRelation{SourceDebateID: debate.ID, TargetDebateID: uuid.New(), Stance: "support", UserID: user.UUID}).Error)

	require.NoError(t, RunDebateWikiMigration(db))

	var migrated model.Debate
	require.NoError(t, db.First(&migrated, "id = ?", debate.ID).Error)
	require.Equal(t, model.DebateStatusActive, migrated.Status)
	require.Empty(t, migrated.ConclusionType)
	require.Nil(t, migrated.CurrentConclusionEventID)
	require.NotNil(t, migrated.CurrentRevisionID)

	var revision model.Revision
	require.NoError(t, db.First(&revision, "content_type = ? AND content_id = ? AND version_number = ?", "debate", debate.ID, 1).Error)
	require.True(t, revision.IsCurrent)
	require.Equal(t, "creation", revision.EditType)
	require.Equal(t, "approved", revision.Status)
	require.Equal(t, user.UUID, revision.EditorID)
	require.Equal(t, debate.CreatedAt.UTC(), revision.CreatedAt.UTC())

	for _, table := range []string{"debate_argument_details", "debate_argument_references", "debate_argument_debate_refs", "vote_histories", "debate_conclude_votes"} {
		require.Falsef(t, db.Migrator().HasTable(table), "legacy table %s should be removed", table)
	}
	var remainingEntries []model.CommentEntry
	require.NoError(t, db.Order("content_hash").Find(&remainingEntries).Error)
	require.Len(t, remainingEntries, 2)
	require.Equal(t, []string{"ordinary-reply", "ordinary-root"}, []string{remainingEntries[0].ContentHash, remainingEntries[1].ContentHash})

	var migratedTarget model.DiscussionTarget
	require.NoError(t, db.First(&migratedTarget, "id = ?", target.ID).Error)
	require.Equal(t, 2, migratedTarget.CommentCount)
	require.Equal(t, 1, migratedTarget.RootCount)
	require.Equal(t, 6, migratedTarget.NextFloor)
	require.Nil(t, migratedTarget.PinnedCommentID)

	for _, table := range []string{"comment_mentions", "comment_attachments", "comment_likes", "comment_reports", "comment_time_anchors", "timeline_revision_proposals"} {
		var count int64
		require.NoError(t, db.Table(table).Count(&count).Error)
		require.EqualValues(t, 1, count, table)
	}
	var argumentNotificationCount, ordinaryNotificationCount, unrelatedNotificationCount int64
	require.NoError(t, db.Model(&model.Notification{}).Where("source_id = ? AND source_type LIKE ?", argument.ID, "comment_%").Count(&argumentNotificationCount).Error)
	require.NoError(t, db.Model(&model.Notification{}).Where("source_id = ? AND source_type LIKE ?", ordinary.ID, "comment_%").Count(&ordinaryNotificationCount).Error)
	require.NoError(t, db.Model(&model.Notification{}).Where("source_id = ? AND source_type = ?", argument.ID, "debate").Count(&unrelatedNotificationCount).Error)
	require.Zero(t, argumentNotificationCount)
	require.EqualValues(t, 1, ordinaryNotificationCount)
	require.EqualValues(t, 1, unrelatedNotificationCount)
	var publishRecordCount int64
	require.NoError(t, db.Model(&model.CommentPublishRecord{}).Where("id = ?", publishRecord.ID).Count(&publishRecordCount).Error)
	require.EqualValues(t, 1, publishRecordCount)
}

func TestRunDebateWikiMigrationIsIdempotentAndKeepsNewProjectionData(t *testing.T) {
	db := testdb.Open(t)
	require.NoError(t, db.AutoMigrate(&model.User{}, &legacyDebate{}))
	user := model.User{UUID: uuid.New(), Username: "owner", Email: "owner@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	debate := legacyDebate{UserID: user.UUID, Title: "Topic", Status: "open", Tags: `{}`}
	require.NoError(t, db.Create(&debate).Error)

	require.NoError(t, RunDebateWikiMigration(db))
	other := model.Debate{UserID: user.UUID, Title: "Source", Status: model.DebateStatusActive}
	require.NoError(t, db.Create(&other).Error)
	event := model.DebateConclusionEvent{DebateID: other.ID, Direction: model.DebateVoteYes, YesVotes: 8, NoVotes: 2, TotalVotes: 10}
	require.NoError(t, db.Create(&event).Error)
	vote := model.DebateVote{DebateID: debate.ID, UserID: user.UUID, Direction: model.DebateVoteYes}
	require.NoError(t, db.Create(&vote).Error)
	relation := model.DebateRelation{SourceDebateID: other.ID, TargetDebateID: debate.ID, Stance: model.DebateRelationSupport, SourceConclusionEventID: event.ID, TargetRevisionID: *mustCurrentRevisionID(t, db, debate.ID), Status: model.DebateRelationActive}
	require.NoError(t, db.Create(&relation).Error)

	require.NoError(t, RunDebateWikiMigration(db))

	var voteCount, relationCount, revisionCount int64
	require.NoError(t, db.Model(&model.DebateVote{}).Count(&voteCount).Error)
	require.NoError(t, db.Model(&model.DebateRelation{}).Count(&relationCount).Error)
	require.NoError(t, db.Model(&model.Revision{}).Where("content_type = ? AND content_id = ? AND version_number = ?", "debate", debate.ID, 1).Count(&revisionCount).Error)
	require.EqualValues(t, 1, voteCount)
	require.EqualValues(t, 1, relationCount)
	require.EqualValues(t, 1, revisionCount)
}

func mustCurrentRevisionID(t *testing.T, db interface{ First(any, ...any) *gorm.DB }, debateID uuid.UUID) *uuid.UUID {
	t.Helper()
	var debate model.Debate
	require.NoError(t, db.First(&debate, "id = ?", debateID).Error)
	require.NotNil(t, debate.CurrentRevisionID)
	return debate.CurrentRevisionID
}
