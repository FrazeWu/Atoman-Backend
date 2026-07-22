package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRunContentReferencesMigrationBackfillsCommentMentionsIdempotently(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.CommentMention{})
	commentID := uuid.New()
	userID := uuid.New()
	require.NoError(t, db.Create(&model.CommentMention{
		CommentID: commentID, UserID: userID, StartOffset: 3, EndOffset: 9,
	}).Error)

	require.NoError(t, RunContentReferencesMigration(db))
	require.NoError(t, RunContentReferencesMigration(db))

	var rows []model.ContentReference
	require.NoError(t, db.Find(&rows).Error)
	require.Equal(t, []model.ContentReference{{
		SourceType: "comment", SourceID: commentID, SourceField: "content",
		TargetType: "user", TargetID: userID, StartOffset: 3, EndOffset: 9,
	}}, withoutReferenceBase(rows))
	assertIndexExists(t, db, "content_references", "uq_content_reference_source_range")
	assertIndexExists(t, db, "content_references", "idx_content_reference_target")
}

func TestContentReferenceIndexesRejectDuplicateRangeAndAllowRepeatedTarget(t *testing.T) {
	db := testdb.Open(t)
	require.NoError(t, RunContentReferencesMigration(db))
	sourceID := uuid.New()
	targetID := uuid.New()
	first := model.ContentReference{
		SourceType: "post", SourceID: sourceID, SourceField: "content",
		TargetType: "user", TargetID: targetID, StartOffset: 0, EndOffset: 6,
	}
	require.NoError(t, db.Create(&first).Error)

	duplicateRange := first
	duplicateRange.ID = uuid.Nil
	require.Error(t, db.Create(&duplicateRange).Error)

	repeatedTarget := first
	repeatedTarget.ID = uuid.Nil
	repeatedTarget.StartOffset = 10
	repeatedTarget.EndOffset = 16
	require.NoError(t, db.Create(&repeatedTarget).Error)
}

func TestRunContentReferencesMigrationBackfillsPublishedLongFormContentSilently(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{}, &model.PodcastEpisode{},
		&model.ForumCategory{}, &model.ForumTopic{}, &model.ForumCategoryPermission{},
		&model.Debate{}, &model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentMention{},
		&model.Notification{},
	)
	actor := model.User{Username: "migration-author", Email: "migration-author@example.com", Password: "hash", IsActive: true}
	mentioned := model.User{Username: "migration-mentioned", Email: "migration-mentioned@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&actor).Error)
	require.NoError(t, db.Create(&mentioned).Error)
	channel := model.Channel{UserID: &actor.UUID, Name: "Migration channel", Slug: "migration-channel"}
	require.NoError(t, db.Create(&channel).Error)
	collection := model.Collection{ChannelID: channel.ID, Name: "Migration collection"}
	require.NoError(t, db.Create(&collection).Error)
	published := model.Post{UserID: actor.UUID, Title: "Published", Content: "@migration-mentioned @channel:" + channel.ID.String(), Status: "published", Visibility: "public"}
	draft := model.Post{UserID: actor.UUID, Title: "Draft", Content: "@migration-mentioned", Status: "draft", Visibility: "public"}
	require.NoError(t, db.Create(&published).Error)
	require.NoError(t, db.Create(&draft).Error)
	category := model.ForumCategory{Name: "Migration forum"}
	require.NoError(t, db.Create(&category).Error)
	topic := model.ForumTopic{UserID: actor.UUID, CategoryID: category.ID, Title: "Topic", Content: "@collection:" + collection.ID.String()}
	require.NoError(t, db.Create(&topic).Error)
	debate := model.Debate{UserID: actor.UUID, Title: "Debate", Description: "@migration-mentioned", Content: "@channel:" + channel.ID.String(), Status: "open"}
	require.NoError(t, db.Create(&debate).Error)
	target := model.DiscussionTarget{Kind: "debate", ResourceID: debate.ID, ResourceKey: debate.ID.String()}
	require.NoError(t, db.Create(&target).Error)
	comment := model.CommentEntry{TargetID: target.ID, AuthorID: actor.UUID, Content: "@channel:" + channel.ID.String(), ContentHash: "migration-comment", Status: "active"}
	require.NoError(t, db.Create(&comment).Error)

	require.NoError(t, RunContentReferencesMigration(db))
	require.NoError(t, RunContentReferencesMigration(db))

	var rows []model.ContentReference
	require.NoError(t, db.Order("source_type ASC, source_id ASC, source_field ASC, start_offset ASC").Find(&rows).Error)
	require.Len(t, rows, 6)
	var draftCount int64
	require.NoError(t, db.Model(&model.ContentReference{}).Where("source_id = ?", draft.ID).Count(&draftCount).Error)
	require.Zero(t, draftCount)
	var notificationCount int64
	require.NoError(t, db.Model(&model.Notification{}).Count(&notificationCount).Error)
	require.Zero(t, notificationCount)
}

func withoutReferenceBase(rows []model.ContentReference) []model.ContentReference {
	result := make([]model.ContentReference, len(rows))
	for index, row := range rows {
		result[index] = model.ContentReference{
			SourceType: row.SourceType, SourceID: row.SourceID, SourceField: row.SourceField,
			TargetType: row.TargetType, TargetID: row.TargetID,
			StartOffset: row.StartOffset, EndOffset: row.EndOffset,
		}
	}
	return result
}
