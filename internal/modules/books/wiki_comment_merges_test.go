package books

import (
	"atoman/internal/model"
	"atoman/internal/testdb"
	"context"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBookWorkMergePreservesBothCommentTreesAndUndo(t *testing.T) {
	service, user, source, target := newBookMergeTestService(t)
	testdb.Migrate(t, service.db, &model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentPublishRecord{})
	require.NoError(t, service.db.Exec("CREATE UNIQUE INDEX test_comment_floor ON comment_entries (target_id,floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL").Error)
	var targets []model.DiscussionTarget
	var roots []model.CommentEntry
	for _, work := range []BookWorkDetail{source, target} {
		discussion := model.DiscussionTarget{Kind: "book_work", ResourceID: uuid.MustParse(work.ID), ResourceKey: work.ID, NextFloor: 2, CommentCount: 1, RootCount: 1}
		require.NoError(t, service.db.Create(&discussion).Error)
		targets = append(targets, discussion)
		floor := 1
		root := model.CommentEntry{TargetID: discussion.ID, AuthorID: user.ID, FloorNumber: &floor, Content: work.Title, ContentHash: work.ID, Status: "active"}
		require.NoError(t, service.db.Create(&root).Error)
		roots = append(roots, root)
	}
	reply := model.CommentEntry{TargetID: targets[0].ID, AuthorID: user.ID, RootID: &roots[0].ID, ReplyToID: &roots[0].ID, Content: "保留回复", ContentHash: "reply", Status: "active"}
	require.NoError(t, service.db.Create(&reply).Error)
	journal := mergeBookTestWorks(t, service, user, source, target)
	var entries []model.CommentEntry
	require.NoError(t, service.db.Where("target_id = ?", targets[1].ID).Find(&entries).Error)
	require.Len(t, entries, 3)
	require.NoError(t, service.db.First(&reply, "id = ?", reply.ID).Error)
	require.Equal(t, roots[0].ID, *reply.RootID)
	_, err := service.RevertBookWorkMerge(context.Background(), user, journal.ID)
	require.NoError(t, err)
	require.NoError(t, service.db.First(&reply, "id = ?", reply.ID).Error)
	require.Equal(t, targets[0].ID, reply.TargetID)
	require.NoError(t, service.db.First(&roots[0], "id = ?", roots[0].ID).Error)
	require.Equal(t, 1, *roots[0].FloorNumber)
}
