package migrations

import (
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestMigrateLegacyForumRepliesPostgres(t *testing.T) {
	db := openLegacyReplyMigrationPostgres(t)
	require.NoError(t, db.AutoMigrate(
		&legacyForumTopic{}, &legacyForumReply{}, &legacyForumLike{},
		&model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentLike{},
		&model.CommentReport{}, &model.CommentMention{}, &model.CommentAttachment{},
		&model.CommentTimeAnchor{}, &model.CommentPublishRecord{},
	))
	require.NoError(t, RunUnifiedCommentIndexes(db))

	values := ids(10)
	ownerID, authorID, likerID, topicID, rootID := values[0], values[1], values[2], values[3], values[4]
	childID, deletedRootID, rescuedRootID, rescuedChildID, likeID := values[5], values[6], values[7], values[8], values[9]
	deleted := gorm.DeletedAt{Time: testMigrationTime(), Valid: true}
	require.NoError(t, db.Create(&legacyForumTopic{Base: model.Base{ID: topicID}, UserID: ownerID, SolvedReplyID: &rootID}).Error)
	require.NoError(t, db.Create(&[]legacyForumReply{
		{Base: model.Base{ID: rootID}, TopicID: topicID, UserID: authorID, Content: "root", FloorNumber: 1, IsSolved: true},
		{Base: model.Base{ID: childID}, TopicID: topicID, UserID: authorID, ParentReplyID: &rootID, Content: "child"},
		{Base: model.Base{ID: deletedRootID, DeletedAt: deleted}, TopicID: topicID, UserID: authorID, Content: "deleted", FloorNumber: 3},
		{Base: model.Base{ID: rescuedRootID}, TopicID: topicID, UserID: authorID, ParentReplyID: &deletedRootID, Content: "rescued"},
		{Base: model.Base{ID: rescuedChildID}, TopicID: topicID, UserID: authorID, ParentReplyID: &rescuedRootID, Content: "rescued child"},
	}).Error)
	require.NoError(t, db.Create(&legacyForumLike{Base: model.Base{ID: likeID}, UserID: likerID, TargetType: "reply", TargetID: rescuedChildID}).Error)

	require.NoError(t, MigrateLegacyForumReplies(db))
	require.NoError(t, MigrateLegacyForumReplies(db))

	var target model.DiscussionTarget
	require.NoError(t, db.Where("kind = ? AND resource_key = ?", forumTopicTargetKind, topicID.String()).First(&target).Error)
	require.Equal(t, 4, target.CommentCount)
	require.Equal(t, 2, target.RootCount)
	require.Equal(t, 5, target.NextFloor)
	require.Equal(t, &rootID, target.PinnedCommentID)
	var rescuedRoot, rescuedChild model.CommentEntry
	require.NoError(t, db.First(&rescuedRoot, "id = ?", rescuedRootID).Error)
	require.NoError(t, db.First(&rescuedChild, "id = ?", rescuedChildID).Error)
	require.Nil(t, rescuedRoot.RootID)
	require.Equal(t, &rescuedRootID, rescuedChild.RootID)
	require.Equal(t, 1, rescuedChild.LikeCount)
	var comments, likes int64
	require.NoError(t, db.Model(&model.CommentEntry{}).Where("target_id = ?", target.ID).Count(&comments).Error)
	require.EqualValues(t, 4, comments)
	require.NoError(t, db.Model(&model.CommentLike{}).Count(&likes).Error)
	require.EqualValues(t, 1, likes)
}

func openLegacyReplyMigrationPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	sqlDB, err := admin.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	schema := "forum_reply_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error })
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func testMigrationTime() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}
