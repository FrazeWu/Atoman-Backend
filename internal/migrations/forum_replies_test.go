package migrations

import (
	"fmt"
	"testing"
	"time"

	"atoman/internal/model"
	commentmodule "atoman/internal/modules/comment"
	"atoman/internal/testdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyForumTopic struct {
	model.Base
	UserID        uuid.UUID
	SolvedReplyID *uuid.UUID
}

func (legacyForumTopic) TableName() string { return "forum_topics" }

type legacyForumReply struct {
	model.Base
	TopicID       uuid.UUID
	UserID        uuid.UUID
	ParentReplyID *uuid.UUID
	Content       string
	FloorNumber   int
	IsSolved      bool
}

func (legacyForumReply) TableName() string { return "forum_replies" }

type legacyForumLike struct {
	model.Base
	UserID     uuid.UUID
	TargetType string
	TargetID   uuid.UUID
}

func (legacyForumLike) TableName() string { return "forum_likes" }

func TestMigrateLegacyForumRepliesNoOpsWithoutLegacyTable(t *testing.T) {
	db := testdb.Open(t)
	require.NoError(t, db.AutoMigrate(&model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentLike{}))
	require.NoError(t, MigrateLegacyForumReplies(db))
}

func TestMigrateLegacyForumRepliesPreservesGraphFieldsSolvedAndLikes(t *testing.T) {
	db := legacyForumReplyDB(t)
	values := ids(14)
	owner, rootAuthor, childAuthor, grandchildAuthor, liker, formerLiker := values[0], values[1], values[2], values[3], values[4], values[5]
	topicID, rootID, childID, grandchildID, deletedID, hiddenChildID, likeID, deletedLikeID := values[6], values[7], values[8], values[9], values[10], values[11], values[12], values[13]
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := created.Add(2 * time.Hour)
	deletedAt := gorm.DeletedAt{Time: updated.Add(time.Hour), Valid: true}

	topic := legacyForumTopic{Base: base(topicID, created, updated, gorm.DeletedAt{}), UserID: owner, SolvedReplyID: &childID}
	rows := []legacyForumReply{
		{Base: base(rootID, created, updated, gorm.DeletedAt{}), TopicID: topicID, UserID: rootAuthor, Content: " cafe\u0301 ", FloorNumber: 7},
		{Base: base(childID, created.Add(time.Minute), updated, gorm.DeletedAt{}), TopicID: topicID, UserID: childAuthor, ParentReplyID: &rootID, Content: "child", FloorNumber: 8},
		{Base: base(grandchildID, created.Add(2*time.Minute), updated, gorm.DeletedAt{}), TopicID: topicID, UserID: grandchildAuthor, ParentReplyID: &childID, Content: "grandchild", FloorNumber: 9},
		{Base: base(deletedID, created.Add(3*time.Minute), updated, deletedAt), TopicID: topicID, UserID: childAuthor, Content: "deleted", FloorNumber: 20},
		{Base: base(hiddenChildID, created.Add(4*time.Minute), updated, gorm.DeletedAt{}), TopicID: topicID, UserID: grandchildAuthor, ParentReplyID: &deletedID, Content: "hidden child", FloorNumber: 21},
	}
	require.NoError(t, db.Create(&topic).Error)
	require.NoError(t, db.Create(&rows).Error)
	require.NoError(t, db.Create(&legacyForumLike{Base: base(likeID, created, updated, gorm.DeletedAt{}), UserID: liker, TargetType: "reply", TargetID: grandchildID}).Error)
	require.NoError(t, db.Create(&legacyForumLike{Base: base(deletedLikeID, created, updated, deletedAt), UserID: formerLiker, TargetType: "reply", TargetID: grandchildID}).Error)

	require.NoError(t, MigrateLegacyForumReplies(db))

	var target model.DiscussionTarget
	require.NoError(t, db.Where("kind = ? AND resource_key = ?", "forum_topic", topicID.String()).First(&target).Error)
	require.Equal(t, topicID, target.ResourceID)
	require.Equal(t, &owner, target.OwnerID)
	require.Equal(t, 4, target.CommentCount)
	require.Equal(t, 3, target.RootCount)
	require.Equal(t, 22, target.NextFloor)
	require.Equal(t, &childID, target.PinnedCommentID)

	var root, child, grandchild, deleted, promotedAfterDelete model.CommentEntry
	require.NoError(t, db.Unscoped().First(&root, "id = ?", rootID).Error)
	require.NoError(t, db.Unscoped().First(&child, "id = ?", childID).Error)
	require.NoError(t, db.Unscoped().First(&grandchild, "id = ?", grandchildID).Error)
	require.NoError(t, db.Unscoped().First(&deleted, "id = ?", deletedID).Error)
	require.NoError(t, db.First(&promotedAfterDelete, "id = ?", hiddenChildID).Error)
	require.Equal(t, " cafe\u0301 ", root.Content)
	require.Equal(t, commentmodule.ContentHash("café", nil), root.ContentHash)
	require.Equal(t, created, root.CreatedAt)
	require.Equal(t, updated, root.UpdatedAt)
	require.Nil(t, root.RootID)
	require.Equal(t, 0, root.ReplyCount)
	require.Nil(t, child.RootID, "solved child must be promoted to a root")
	require.Nil(t, child.ReplyToID)
	require.NotNil(t, child.FloorNumber)
	require.Equal(t, 1, child.ReplyCount)
	require.Greater(t, child.HotScore, 0.0)
	require.Equal(t, &childID, grandchild.RootID)
	require.Equal(t, &childID, grandchild.ReplyToID)
	require.Equal(t, &childAuthor, grandchild.ReplyToAuthorID)
	require.Nil(t, grandchild.FloorNumber)
	require.Equal(t, 1, grandchild.LikeCount)
	require.True(t, deleted.DeletedAt.Valid)
	require.Zero(t, deleted.ReplyCount, "children of an invisible root must not affect visible counts")
	require.Nil(t, promotedAfterDelete.RootID)
	require.Nil(t, promotedAfterDelete.ReplyToID)
	require.NotNil(t, promotedAfterDelete.FloorNumber)
	var visibleRootIDs []uuid.UUID
	require.NoError(t, db.Model(&model.CommentEntry{}).
		Where("target_id = ? AND root_id IS NULL AND status IN ?", target.ID, []string{"active", "auto_folded"}).
		Pluck("id", &visibleRootIDs).Error)
	require.Contains(t, visibleRootIDs, hiddenChildID)

	var migratedLike model.CommentLike
	require.NoError(t, db.First(&migratedLike, "id = ?", likeID).Error)
	require.Equal(t, grandchildID, migratedLike.CommentID)
	require.Equal(t, liker, migratedLike.UserID)
	var deletedLike model.CommentLike
	require.NoError(t, db.Unscoped().First(&deletedLike, "id = ?", deletedLikeID).Error)
	require.True(t, deletedLike.DeletedAt.Valid)
	require.Equal(t, created, deletedLike.CreatedAt)
	require.Equal(t, updated, deletedLike.UpdatedAt)
}

func TestMigrateLegacyForumRepliesIsIdempotentAndKeepsUnifiedEdits(t *testing.T) {
	db := legacyForumReplyDB(t)
	values := ids(8)
	owner, author, topicID, replyID := values[0], values[1], values[2], values[3]
	likeID, likerID, replacementOwnerID, replacementPinID := values[4], values[5], values[6], values[7]
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	require.NoError(t, db.Create(&legacyForumTopic{Base: base(topicID, now, now, gorm.DeletedAt{}), UserID: owner, SolvedReplyID: &replyID}).Error)
	require.NoError(t, db.Create(&legacyForumReply{Base: base(replyID, now, now, gorm.DeletedAt{}), TopicID: topicID, UserID: author, Content: "legacy", FloorNumber: 1, IsSolved: true}).Error)
	require.NoError(t, db.Create(&legacyForumLike{Base: base(likeID, now, now, gorm.DeletedAt{}), UserID: likerID, TargetType: "reply", TargetID: replyID}).Error)
	require.NoError(t, MigrateLegacyForumReplies(db))

	var target model.DiscussionTarget
	require.NoError(t, db.Where("kind = ? AND resource_key = ?", "forum_topic", topicID.String()).First(&target).Error)
	require.Equal(t, &replyID, target.PinnedCommentID, "legacy solved reply is applied on first migration")
	replacementFloor := 2
	require.NoError(t, db.Create(&model.CommentEntry{Base: model.Base{ID: replacementPinID}, TargetID: target.ID, AuthorID: author, FloorNumber: &replacementFloor, Content: "replacement", ContentHash: "replacement", Status: "active"}).Error)
	deletedAt := now.Add(24 * time.Hour)
	require.NoError(t, db.Unscoped().Model(&model.DiscussionTarget{}).Where("id = ?", target.ID).UpdateColumns(map[string]any{
		"owner_id":          replacementOwnerID,
		"pinned_comment_id": replacementPinID,
		"deleted_at":        deletedAt,
	}).Error)
	require.NoError(t, db.Model(&model.CommentEntry{}).Where("id = ?", replyID).Updates(map[string]any{"content": "new edit", "content_hash": "new-hash"}).Error)
	require.NoError(t, MigrateLegacyForumReplies(db))

	var entry model.CommentEntry
	require.NoError(t, db.First(&entry, "id = ?", replyID).Error)
	require.Equal(t, "new edit", entry.Content)
	require.Equal(t, "new-hash", entry.ContentHash)
	require.NoError(t, db.Unscoped().First(&target, "id = ?", target.ID).Error)
	require.Equal(t, &replacementOwnerID, target.OwnerID)
	require.True(t, target.DeletedAt.Valid)
	require.Equal(t, &replacementPinID, target.PinnedCommentID)

	targetID := target.ID
	require.NoError(t, db.Unscoped().Model(&model.DiscussionTarget{}).Where("id = ?", targetID).UpdateColumn("pinned_comment_id", nil).Error)
	require.NoError(t, MigrateLegacyForumReplies(db))
	target = model.DiscussionTarget{}
	require.NoError(t, db.Unscoped().First(&target, "id = ?", targetID).Error)
	require.Nil(t, target.PinnedCommentID, "a unified-system unpin must survive later startup migrations")
	require.Equal(t, &replacementOwnerID, target.OwnerID)
	require.True(t, target.DeletedAt.Valid)

	var count int64
	require.NoError(t, db.Model(&model.CommentEntry{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
	require.NoError(t, db.Unscoped().Table("forum_replies").Count(&count).Error)
	require.EqualValues(t, 1, count, "legacy replies must remain in place")
	require.NoError(t, db.Unscoped().Table("forum_likes").Where("target_type = ?", "reply").Count(&count).Error)
	require.EqualValues(t, 1, count, "legacy reply likes must remain in place")
}

func TestMigrateLegacyForumRepliesReusesTargetAndRenumbersDuplicateFloors(t *testing.T) {
	db := legacyForumReplyDB(t)
	values := ids(6)
	owner, author, topicID, firstID, secondID, targetID := values[0], values[1], values[2], values[3], values[4], values[5]
	existingID := uuid.New()
	now := time.Now().UTC()
	require.NoError(t, db.Create(&legacyForumTopic{Base: base(topicID, now, now, gorm.DeletedAt{}), UserID: owner}).Error)
	target := model.DiscussionTarget{Base: model.Base{ID: targetID}, Kind: "forum_topic", ResourceID: topicID, ResourceKey: topicID.String(), OwnerID: &owner, NextFloor: 1}
	require.NoError(t, db.Create(&target).Error)
	existingFloor := 1
	require.NoError(t, db.Create(&model.CommentEntry{Base: model.Base{ID: existingID}, TargetID: targetID, AuthorID: author, FloorNumber: &existingFloor, Content: "unified", ContentHash: "unified", Status: "active"}).Error)
	require.NoError(t, db.Create(&[]legacyForumReply{
		{Base: base(firstID, now, now, gorm.DeletedAt{}), TopicID: topicID, UserID: author, Content: "one", FloorNumber: 1},
		{Base: base(secondID, now.Add(time.Second), now, gorm.DeletedAt{}), TopicID: topicID, UserID: author, Content: "two", FloorNumber: 1},
	}).Error)
	require.NoError(t, MigrateLegacyForumReplies(db))

	var entries []model.CommentEntry
	require.NoError(t, db.Order("created_at, id").Find(&entries).Error)
	require.Len(t, entries, 3)
	var first, second model.CommentEntry
	require.NoError(t, db.First(&first, "id = ?", firstID).Error)
	require.NoError(t, db.First(&second, "id = ?", secondID).Error)
	require.Equal(t, targetID, first.TargetID)
	require.Equal(t, 2, *first.FloorNumber)
	require.Equal(t, 3, *second.FloorNumber)
}

func TestPreviouslyMigratedForumTopicsReusesLoadedComments(t *testing.T) {
	topicID, authorID, replyID := uuid.New(), uuid.New(), uuid.New()
	replies := []legacyReplyMigrationReply{{Base: model.Base{ID: replyID}, TopicID: topicID, UserID: authorID}}
	migrated := previouslyMigratedForumTopics(replies, map[uuid.UUID]model.CommentEntry{replyID: {Base: model.Base{ID: replyID}}})
	require.True(t, migrated[topicID])
}

func TestLoadLegacyCommentEntriesBatchesReplyIDs(t *testing.T) {
	db := legacyForumReplyDB(t)
	topicID, targetID, authorID := uuid.New(), uuid.New(), uuid.New()
	replies := make([]legacyReplyMigrationReply, 501)
	for i := range replies {
		replies[i] = legacyReplyMigrationReply{Base: model.Base{ID: uuid.New()}, TopicID: topicID, UserID: authorID}
	}
	require.NoError(t, db.Create(&model.DiscussionTarget{Base: model.Base{ID: targetID}, Kind: "forum_topic", ResourceID: topicID, ResourceKey: topicID.String(), NextFloor: 1}).Error)
	require.NoError(t, db.Create(&model.CommentEntry{Base: replies[500].Base, TargetID: targetID, AuthorID: authorID, Content: "existing", ContentHash: "existing", Status: "active"}).Error)
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("reject_large_legacy_comment_in", func(tx *gorm.DB) {
		if tx.Statement.Table == "comment_entries" && len(tx.Statement.Vars) > 500 {
			tx.AddError(fmt.Errorf("too many bound variables: %d", len(tx.Statement.Vars)))
		}
	}))

	existing, err := loadLegacyCommentEntries(db, replies)
	require.NoError(t, err)
	require.Contains(t, existing, replies[500].ID)
}

func TestLoadLegacyForumTopicsBatchesTopicIDs(t *testing.T) {
	db := legacyForumReplyDB(t)
	topicIDs := make([]uuid.UUID, 501)
	for i := range topicIDs {
		topicIDs[i] = uuid.New()
	}
	require.NoError(t, db.Create(&legacyForumTopic{Base: model.Base{ID: topicIDs[500]}, UserID: uuid.New()}).Error)
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("reject_large_legacy_topic_in", func(tx *gorm.DB) {
		if tx.Statement.Table == "forum_topics" && len(tx.Statement.Vars) > 500 {
			tx.AddError(fmt.Errorf("too many bound variables: %d", len(tx.Statement.Vars)))
		}
	}))

	topics, err := loadLegacyForumTopics(db, topicIDs)
	require.NoError(t, err)
	require.Len(t, topics, 1)
	require.Equal(t, topicIDs[500], topics[0].ID)
}

func TestMigrateLegacyForumRepliesUsesBoundedReadQueries(t *testing.T) {
	db := legacyForumReplyDB(t)
	ownerID, authorID, likerID := uuid.New(), uuid.New(), uuid.New()
	topics := make([]legacyForumTopic, 20)
	replies := make([]legacyForumReply, 20)
	likes := make([]legacyForumLike, 10)
	for i := range replies {
		topics[i] = legacyForumTopic{Base: model.Base{ID: uuid.New()}, UserID: ownerID}
		replies[i] = legacyForumReply{Base: model.Base{ID: uuid.New()}, TopicID: topics[i].ID, UserID: authorID, Content: fmt.Sprintf("reply %d", i), FloorNumber: 1}
		if i < len(likes) {
			likes[i] = legacyForumLike{Base: model.Base{ID: uuid.New()}, UserID: likerID, TargetType: "reply", TargetID: replies[i].ID}
		}
	}
	require.NoError(t, db.Create(&topics).Error)
	require.NoError(t, db.Create(&replies).Error)
	require.NoError(t, db.Create(&likes).Error)
	queryCounts := map[string]int{}
	updateCounts := map[string]int{}
	createCounts := map[string]int{}
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("count_legacy_migration_queries", func(tx *gorm.DB) {
		queryCounts[tx.Statement.Table]++
	}))
	require.NoError(t, db.Callback().Update().After("gorm:update").Register("count_legacy_migration_updates", func(tx *gorm.DB) {
		updateCounts[tx.Statement.Table]++
	}))
	require.NoError(t, db.Callback().Create().After("gorm:create").Register("count_legacy_migration_creates", func(tx *gorm.DB) {
		createCounts[tx.Statement.Table]++
	}))

	require.NoError(t, MigrateLegacyForumReplies(db))
	require.LessOrEqual(t, queryCounts["comment_entries"], 3, queryCounts)
	require.LessOrEqual(t, queryCounts["comment_likes"], 3, queryCounts)
	require.LessOrEqual(t, queryCounts["discussion_targets"], 2, queryCounts)
	require.LessOrEqual(t, updateCounts["comment_entries"], 3, updateCounts)
	require.LessOrEqual(t, createCounts["comment_entries"], 3, createCounts)
	require.LessOrEqual(t, updateCounts["discussion_targets"], 3, updateCounts)
	require.LessOrEqual(t, createCounts["discussion_targets"], 3, createCounts)
}

func TestMigrateLegacyForumRepliesRollsBackInvalidLegacyData(t *testing.T) {
	tests := map[string]func(t *testing.T, db *gorm.DB, topicID, firstID, secondID, author uuid.UUID){
		"orphan parent": func(t *testing.T, db *gorm.DB, topicID, firstID, secondID, author uuid.UUID) {
			missing := uuid.New()
			require.NoError(t, db.Create(&legacyForumReply{Base: model.Base{ID: firstID}, TopicID: topicID, UserID: author, ParentReplyID: &missing, Content: "bad"}).Error)
		},
		"cycle": func(t *testing.T, db *gorm.DB, topicID, firstID, secondID, author uuid.UUID) {
			require.NoError(t, db.Create(&[]legacyForumReply{
				{Base: model.Base{ID: firstID}, TopicID: topicID, UserID: author, ParentReplyID: &secondID, Content: "one"},
				{Base: model.Base{ID: secondID}, TopicID: topicID, UserID: author, ParentReplyID: &firstID, Content: "two"},
			}).Error)
		},
		"orphan like": func(t *testing.T, db *gorm.DB, topicID, firstID, secondID, author uuid.UUID) {
			require.NoError(t, db.Create(&legacyForumReply{Base: model.Base{ID: firstID}, TopicID: topicID, UserID: author, Content: "valid reply"}).Error)
			require.NoError(t, db.Create(&legacyForumLike{Base: model.Base{ID: uuid.New()}, UserID: author, TargetType: "reply", TargetID: secondID}).Error)
		},
	}
	for name, seed := range tests {
		t.Run(name, func(t *testing.T) {
			db := legacyForumReplyDB(t)
			values := ids(5)
			owner, author, topicID, firstID, secondID := values[0], values[1], values[2], values[3], values[4]
			require.NoError(t, db.Create(&legacyForumTopic{Base: model.Base{ID: topicID}, UserID: owner}).Error)
			seed(t, db, topicID, firstID, secondID, author)
			err := MigrateLegacyForumReplies(db)
			require.Error(t, err)
			var targets int64
			require.NoError(t, db.Model(&model.DiscussionTarget{}).Count(&targets).Error)
			require.Zero(t, targets)
		})
	}
}

func TestLegacyReplyGraphAndRootResolutionHandleLongChain(t *testing.T) {
	topicID, authorID := uuid.New(), uuid.New()
	replies := make([]legacyReplyMigrationReply, 5000)
	byID := make(map[uuid.UUID]legacyReplyMigrationReply, len(replies))
	for i := range replies {
		replies[i] = legacyReplyMigrationReply{Base: model.Base{ID: uuid.New()}, TopicID: topicID, UserID: authorID}
		if i > 0 {
			parentID := replies[i-1].ID
			replies[i].ParentReplyID = &parentID
		}
		byID[replies[i].ID] = replies[i]
	}
	require.NoError(t, validateLegacyReplyGraph(replies, byID))
	roots := resolveLegacyReplyRoots(replies, byID, nil)
	require.Equal(t, replies[0].ID, roots[replies[len(replies)-1].ID])
	require.Len(t, roots, len(replies))
}

func TestMigrateLegacyForumRepliesRejectsCrossTopicParentAndUUIDCollision(t *testing.T) {
	t.Run("cross topic", func(t *testing.T) {
		db := legacyForumReplyDB(t)
		values := ids(6)
		owner, author, topicA, topicB, parentID, childID := values[0], values[1], values[2], values[3], values[4], values[5]
		require.NoError(t, db.Create(&[]legacyForumTopic{{Base: model.Base{ID: topicA}, UserID: owner}, {Base: model.Base{ID: topicB}, UserID: owner}}).Error)
		require.NoError(t, db.Create(&[]legacyForumReply{
			{Base: model.Base{ID: parentID}, TopicID: topicA, UserID: author, Content: "parent"},
			{Base: model.Base{ID: childID}, TopicID: topicB, UserID: author, ParentReplyID: &parentID, Content: "child"},
		}).Error)
		require.ErrorContains(t, MigrateLegacyForumReplies(db), "cross-topic")
	})

	t.Run("comment UUID", func(t *testing.T) {
		db := legacyForumReplyDB(t)
		values := ids(6)
		owner, author, otherAuthor, topicID, replyID, targetID := values[0], values[1], values[2], values[3], values[4], values[5]
		require.NoError(t, db.Create(&legacyForumTopic{Base: model.Base{ID: topicID}, UserID: owner}).Error)
		require.NoError(t, db.Create(&legacyForumReply{Base: model.Base{ID: replyID}, TopicID: topicID, UserID: author, Content: "legacy"}).Error)
		require.NoError(t, db.Create(&model.DiscussionTarget{Base: model.Base{ID: targetID}, Kind: "forum_topic", ResourceID: topicID, ResourceKey: topicID.String(), OwnerID: &owner, NextFloor: 1}).Error)
		require.NoError(t, db.Create(&model.CommentEntry{Base: model.Base{ID: replyID}, TargetID: targetID, AuthorID: otherAuthor, Content: "existing", ContentHash: "x", Status: "active"}).Error)
		require.ErrorContains(t, MigrateLegacyForumReplies(db), "UUID collision")
	})
}

func legacyForumReplyDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	require.NoError(t, db.AutoMigrate(
		&legacyForumTopic{}, &legacyForumReply{}, &legacyForumLike{},
		&model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentLike{},
		&model.CommentReport{}, &model.CommentMention{}, &model.CommentAttachment{},
		&model.CommentTimeAnchor{}, &model.CommentPublishRecord{},
	))
	require.NoError(t, RunUnifiedCommentIndexes(db))
	return db
}

func base(id uuid.UUID, created, updated time.Time, deleted gorm.DeletedAt) model.Base {
	return model.Base{ID: id, CreatedAt: created, UpdatedAt: updated, DeletedAt: deleted}
}

func ids(count int) []uuid.UUID {
	result := make([]uuid.UUID, count)
	for i := range result {
		result[i] = uuid.New()
	}
	return result
}
