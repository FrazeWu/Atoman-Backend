package comment

import (
	"errors"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEditRevalidatesAndReplacesRelationsWithoutChangingThreadIdentity(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindVideo, 500)
	oldAsset := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/png")
	oldMention := MentionInput{UserID: ctx.users[1].ID, Start: 0, End: len([]rune("@comment-user-1"))}
	created, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "@comment-user-1 0:12", Mentions: []MentionInput{oldMention}, AttachmentIDs: []uuid.UUID{oldAsset.ID}})
	require.NoError(t, err)
	require.NoError(t, ctx.service.Like(ctx.users[0], created.ID))
	before := time.Now()
	newAsset := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/webp")
	newMention := MentionInput{UserID: ctx.users[2].ID, Start: 0, End: len([]rune("@comment-user-2"))}

	edited, err := ctx.service.Edit(ctx.users[0], created.ID, EditCommentInput{Content: " @comment-user-2 cafe\u0301 1:02 9:00 ", Mentions: []MentionInput{newMention}, AttachmentIDs: []uuid.UUID{newAsset.ID}})
	require.NoError(t, err)
	require.Equal(t, "@comment-user-2 café 1:02 9:00", edited.Content)
	require.NotNil(t, edited.EditedAt)
	require.True(t, edited.Liked)
	require.False(t, edited.EditedAt.Before(before))
	require.Len(t, edited.Mentions, 1)
	require.Equal(t, ctx.users[2].ID, edited.Mentions[0].UserID)
	require.Len(t, edited.Attachments, 1)
	require.Equal(t, newAsset.ID, edited.Attachments[0].ID)
	require.Len(t, edited.TimeAnchors, 1)
	require.Equal(t, 62, edited.TimeAnchors[0].Seconds)

	var stored model.CommentEntry
	require.NoError(t, ctx.db.First(&stored, "id = ?", created.ID).Error)
	require.Equal(t, created.CreatedAt, stored.CreatedAt)
	require.Equal(t, created.RootID, stored.RootID)
	require.Equal(t, created.ReplyToID, stored.ReplyToID)
	require.Equal(t, created.FloorNumber, stored.FloorNumber)
	require.Equal(t, ContentHash(edited.Content, []uuid.UUID{newAsset.ID}), stored.ContentHash)
	for _, table := range []any{&model.CommentMention{}, &model.CommentAttachment{}, &model.CommentTimeAnchor{}} {
		var count int64
		require.NoError(t, ctx.db.Unscoped().Model(table).Where("comment_id = ? AND deleted_at IS NOT NULL", created.ID).Count(&count).Error)
		require.Zero(t, count, "old relations must be physically replaced")
	}

	_, err = ctx.service.Edit(ctx.users[1], created.ID, EditCommentInput{Content: "forbidden"})
	require.ErrorIs(t, err, ErrCommentForbidden)
	_, err = ctx.service.Edit(ctx.users[0], created.ID, EditCommentInput{Content: strings.Repeat("界", 2001)})
	require.ErrorIs(t, err, ErrInvalidContent)
	imageOnly, err := ctx.service.Edit(ctx.users[0], created.ID, EditCommentInput{AttachmentIDs: []uuid.UUID{newAsset.ID}})
	require.NoError(t, err)
	require.Empty(t, imageOnly.Content)
}

func TestDeleteRootPhysicallyDeletesBuildingRelationsAndCounters(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 1, "root", nil)
	child := ctx.create(t, 2, "child", &root.ID)
	grandchild := ctx.create(t, 3, "reply child", &child.ID)
	require.NoError(t, ctx.service.Like(ctx.users[0], root.ID))
	require.NoError(t, ctx.db.Create(&model.CommentReport{CommentID: child.ID, ReporterID: ctx.users[0].ID, Reason: "spam"}).Error)
	require.NoError(t, ctx.db.Create(&model.CommentMention{CommentID: grandchild.ID, UserID: ctx.users[0].ID, StartOffset: 0, EndOffset: 1}).Error)
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.First(&target).Error)
	require.NoError(t, ctx.db.Model(&target).Update("pinned_comment_id", root.ID).Error)

	require.NoError(t, ctx.service.Delete(ctx.users[0], root.ID)) // target owner
	ids := []uuid.UUID{root.ID, child.ID, grandchild.ID}
	var commentCount int64
	require.NoError(t, ctx.db.Unscoped().Model(&model.CommentEntry{}).Where("id IN ?", ids).Count(&commentCount).Error)
	require.Zero(t, commentCount)
	for _, table := range []any{&model.CommentMention{}, &model.CommentAttachment{}, &model.CommentLike{}, &model.CommentReport{}, &model.CommentTimeAnchor{}} {
		var count int64
		require.NoError(t, ctx.db.Unscoped().Model(table).Where("comment_id IN ?", ids).Count(&count).Error)
		require.Zero(t, count)
	}
	var updatedTarget model.DiscussionTarget
	require.NoError(t, ctx.db.First(&updatedTarget, "id = ?", target.ID).Error)
	require.Zero(t, updatedTarget.CommentCount)
	require.Zero(t, updatedTarget.RootCount)
	require.Nil(t, updatedTarget.PinnedCommentID)
	require.ErrorIs(t, ctx.service.Delete(ctx.users[0], root.ID), ErrCommentNotFound)
}

func TestDeleteChildKeepsThreadAndHistoricalReplyReference(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "root", nil)
	child := ctx.create(t, 1, "child", &root.ID)
	later := ctx.create(t, 2, "later", &child.ID)
	require.NoError(t, ctx.service.Delete(ctx.users[1], child.ID))
	var remaining model.CommentEntry
	require.NoError(t, ctx.db.First(&remaining, "id = ?", later.ID).Error)
	require.Equal(t, child.ID, *remaining.ReplyToID)
	var storedRoot model.CommentEntry
	require.NoError(t, ctx.db.First(&storedRoot, "id = ?", root.ID).Error)
	require.Equal(t, 1, storedRoot.ReplyCount)
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.First(&target).Error)
	require.Equal(t, 2, target.CommentCount)
	require.Equal(t, 1, target.RootCount)
}

func TestDeletePermissionsRequireActiveAuthorOrTargetOwner(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 1, "root", nil)
	admin := ctx.users[2]
	admin.Role = authctx.RoleAdmin
	require.ErrorIs(t, ctx.service.Delete(admin, root.ID), ErrCommentForbidden)
	require.ErrorIs(t, ctx.service.Delete(authctx.CurrentUser{}, root.ID), ErrAuthenticationRequired)
	require.NoError(t, ctx.db.Model(&model.User{}).Where("uuid = ?", ctx.users[1].ID).Update("is_active", false).Error)
	require.ErrorIs(t, ctx.service.Delete(ctx.users[1], root.ID), ErrAuthenticationRequired)

	ownerless := ctx.resolved
	ownerless.OwnerID = nil
	ctx.service.registry.resolvers[TargetKindBlogPost] = targetResolverFunc(func(Viewer, uuid.UUID) (ResolvedTarget, error) { return ownerless, nil })
	root2 := ctx.create(t, 2, "ownerless", nil)
	require.ErrorIs(t, ctx.service.Delete(ctx.users[0], root2.ID), ErrCommentForbidden)
	require.NoError(t, ctx.service.Delete(ctx.users[2], root2.ID))
}

func TestLikeUnlikeAreIdempotentUpdateViewerStateAndHotScore(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "root", nil)
	child := ctx.create(t, 1, "child", &root.ID)
	var scored model.CommentEntry
	require.NoError(t, ctx.db.First(&scored, "id = ?", root.ID).Error)
	require.InDelta(t, HotScore(0, 0, 1, time.Since(scored.CreatedAt)), scored.HotScore, .01)
	require.NoError(t, ctx.service.Like(ctx.users[2], child.ID))
	require.NoError(t, ctx.service.Like(ctx.users[2], child.ID))
	require.NoError(t, ctx.service.Like(ctx.users[3], root.ID))
	require.NoError(t, ctx.db.First(&scored, "id = ?", root.ID).Error)
	require.Equal(t, 1, scored.LikeCount)
	require.Greater(t, scored.HotScore, 0.0)
	var storedChild model.CommentEntry
	require.NoError(t, ctx.db.First(&storedChild, "id = ?", child.ID).Error)
	require.Equal(t, 1, storedChild.LikeCount)

	listed, err := ctx.service.List(ctx.users[2], ctx.target, ListCommentsInput{Page: 1})
	require.NoError(t, err)
	require.False(t, listed.Items[0].Liked)
	require.True(t, listed.Items[0].Replies[0].Liked)
	require.NoError(t, ctx.service.Unlike(ctx.users[2], child.ID))
	require.NoError(t, ctx.service.Unlike(ctx.users[2], child.ID))
	require.NoError(t, ctx.service.Like(ctx.users[2], child.ID))
	require.NoError(t, ctx.db.First(&storedChild, "id = ?", child.ID).Error)
	require.Equal(t, 1, storedChild.LikeCount)

	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", child.ID).Update("status", "hidden").Error)
	require.ErrorIs(t, ctx.service.Like(ctx.users[0], child.ID), ErrCommentNotFound)
}

func TestMarkRequiresTargetOwnerAndActiveRootAndAtomicallyReplaces(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindForumTopic, 0)
	first := ctx.create(t, 1, "first", nil)
	second := ctx.create(t, 2, "second", nil)
	child := ctx.create(t, 3, "child", &first.ID)
	require.ErrorIs(t, ctx.service.Mark(ctx.users[1], ctx.target, first.ID), ErrCommentForbidden)
	admin := ctx.users[1]
	admin.Role = authctx.RoleAdmin
	require.ErrorIs(t, ctx.service.Mark(admin, ctx.target, first.ID), ErrCommentForbidden)
	require.ErrorIs(t, ctx.service.Mark(ctx.users[0], ctx.target, child.ID), ErrInvalidMark)
	require.NoError(t, ctx.service.Mark(ctx.users[0], ctx.target, first.ID))
	require.NoError(t, ctx.service.Mark(ctx.users[0], ctx.target, first.ID))
	require.NoError(t, ctx.service.Mark(ctx.users[0], ctx.target, second.ID))
	listed, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1})
	require.NoError(t, err)
	require.Equal(t, second.ID, listed.Items[0].ID)
	require.True(t, listed.Items[0].Marked)

	other := newCommentTestContext(t, TargetKindBlogPost, 0)
	foreign := other.create(t, 0, "foreign", nil)
	require.ErrorIs(t, ctx.service.Mark(ctx.users[0], ctx.target, foreign.ID), ErrInvalidMark)
	require.NoError(t, ctx.service.Unmark(ctx.users[0], ctx.target))

	ownerless := ctx.resolved
	ownerless.OwnerID = nil
	ctx.service.registry.resolvers[TargetKindForumTopic] = targetResolverFunc(func(Viewer, uuid.UUID) (ResolvedTarget, error) { return ownerless, nil })
	require.ErrorIs(t, ctx.service.Mark(ctx.users[0], ctx.target, first.ID), ErrCommentForbidden)
}

func TestInteractionErrorsAreComparable(t *testing.T) {
	require.True(t, errors.Is(ErrCommentForbidden, ErrCommentForbidden))
	require.True(t, errors.Is(ErrCommentNotFound, ErrCommentNotFound))
	require.True(t, errors.Is(ErrInvalidMark, ErrInvalidMark))
}
