package comment

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
	after := time.Now()
	require.NoError(t, err)
	require.Equal(t, "@comment-user-2 café 1:02 9:00", edited.Content)
	require.NotNil(t, edited.EditedAt)
	require.True(t, edited.Liked)
	require.False(t, edited.EditedAt.Before(before))
	require.False(t, edited.EditedAt.After(after))
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

func TestDeleteRootPhysicallyDeletesMixedBuildingAndDecrementsOnlyVisibleCounters(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 1, "root", nil)
	active := ctx.create(t, 2, "active", &root.ID)
	folded := ctx.create(t, 3, "folded", &active.ID)
	hidden := ctx.create(t, 0, "hidden", &root.ID)
	softDeleted := ctx.create(t, 2, "soft deleted", &root.ID)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", folded.ID).Update("status", "auto_folded").Error)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", hidden.ID).Update("status", "moderator_hidden").Error)
	require.NoError(t, ctx.db.Delete(&model.CommentEntry{}, "id = ?", softDeleted.ID).Error)
	require.NoError(t, ctx.service.Like(ctx.users[0], root.ID))
	require.NoError(t, ctx.db.Create(&model.CommentReport{CommentID: active.ID, ReporterID: ctx.users[0].ID, Reason: "spam"}).Error)
	require.NoError(t, ctx.db.Create(&model.CommentMention{CommentID: folded.ID, UserID: ctx.users[0].ID, StartOffset: 0, EndOffset: 1}).Error)
	asset := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/png")
	require.NoError(t, ctx.db.Create(&model.CommentAttachment{CommentID: hidden.ID, MediaAssetID: asset.ID}).Error)
	require.NoError(t, ctx.db.Create(&model.CommentTimeAnchor{CommentID: softDeleted.ID, StartOffset: 0, EndOffset: 4, Seconds: 12}).Error)
	require.NoError(t, ctx.db.Create(&model.TimelineRevisionProposal{CommentID: root.ID, TargetKind: "event", TargetID: uuid.New(), PatchJSON: json.RawMessage(`[]`), Evidence: "source"}).Error)
	require.NoError(t, ctx.db.Create(&model.DebateArgumentDetail{CommentID: active.ID, ArgumentType: "claim"}).Error)
	require.NoError(t, ctx.db.Create(&model.DebateArgumentReference{CommentID: folded.ID, ReferencedCommentID: active.ID}).Error)
	require.NoError(t, ctx.db.Create(&model.DebateArgumentDebateRef{CommentID: hidden.ID, DebateID: uuid.New()}).Error)
	external := ctx.create(t, 0, "external root", nil)
	require.NoError(t, ctx.db.Create(&model.DebateArgumentReference{CommentID: external.ID, ReferencedCommentID: folded.ID}).Error)
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.First(&target).Error)
	require.NoError(t, ctx.db.Model(&target).Updates(map[string]any{"pinned_comment_id": root.ID, "comment_count": 4, "root_count": 2}).Error)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", root.ID).Update("reply_count", 2).Error)

	require.NoError(t, ctx.service.Delete(ctx.users[0], root.ID)) // target owner
	ids := []uuid.UUID{root.ID, active.ID, folded.ID, hidden.ID, softDeleted.ID}
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
	require.Equal(t, 1, updatedTarget.CommentCount)
	require.Equal(t, 1, updatedTarget.RootCount)
	require.Nil(t, updatedTarget.PinnedCommentID)
	require.ErrorIs(t, ctx.service.Delete(ctx.users[0], root.ID), ErrCommentNotFound)
	for _, table := range []any{&model.TimelineRevisionProposal{}, &model.DebateArgumentDetail{}, &model.DebateArgumentReference{}, &model.DebateArgumentDebateRef{}} {
		var count int64
		require.NoError(t, ctx.db.Unscoped().Model(table).Count(&count).Error)
		require.Zero(t, count)
	}
}

func TestListRefreshesRepresentativeResourceAndMutationsResolveItAgain(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindFeedArticle, 0)
	primaryID := ctx.target.ResourceID
	alternateID := uuid.New()
	visible := true
	missing := false
	canonicalKey := ctx.resolved.ResourceKey
	ctx.service.registry.resolvers[TargetKindFeedArticle] = targetResolverFunc(func(_ Viewer, id uuid.UUID) (ResolvedTarget, error) {
		if id != primaryID && id != alternateID {
			return ResolvedTarget{}, ErrTargetNotFound
		}
		if missing {
			return ResolvedTarget{}, ErrTargetNotFound
		}
		return ResolvedTarget{Kind: TargetKindFeedArticle, ResourceID: id, ResourceKey: canonicalKey, Visible: visible}, nil
	})
	created := ctx.create(t, 0, "shared RSS comment", nil)
	alternateRef := TargetRef{Kind: TargetKindFeedArticle, ResourceID: alternateID}
	_, err := ctx.service.List(ctx.users[0], alternateRef, ListCommentsInput{Page: 1})
	require.NoError(t, err)
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.First(&target).Error)
	require.Equal(t, alternateID, target.ResourceID)
	require.NoError(t, ctx.service.Like(ctx.users[1], created.ID))

	visible = false
	require.ErrorIs(t, ctx.service.Unlike(ctx.users[1], created.ID), ErrTargetNotVisible)
	missing = true
	_, err = ctx.service.Edit(ctx.users[0], created.ID, EditCommentInput{Content: "blocked"})
	require.ErrorIs(t, err, ErrTargetNotFound)
	missing = false
	visible = true
	canonicalKey = "https://example.com/changed"
	require.ErrorIs(t, ctx.service.Unlike(ctx.users[1], created.ID), ErrInvalidTargetResource)
}

func TestConcurrentCanonicalListsRefreshRepresentativeIdempotently(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindFeedArticle, 0)
	primaryID := ctx.target.ResourceID
	mirrorIDs := []uuid.UUID{uuid.New(), uuid.New()}
	canonicalKey := ctx.resolved.ResourceKey
	ctx.service.registry.resolvers[TargetKindFeedArticle] = targetResolverFunc(func(_ Viewer, id uuid.UUID) (ResolvedTarget, error) {
		if id != primaryID && id != mirrorIDs[0] && id != mirrorIDs[1] {
			return ResolvedTarget{}, ErrTargetNotFound
		}
		return ResolvedTarget{Kind: TargetKindFeedArticle, ResourceID: id, ResourceKey: canonicalKey, Visible: true}, nil
	})
	ctx.create(t, 0, "shared", nil)

	for round := 0; round < 5; round++ {
		arrived := make(chan struct{}, 2)
		release := make(chan struct{})
		callbackName := "rss-list-barrier-" + uuid.NewString()
		require.NoError(t, ctx.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == "discussion_targets" {
				arrived <- struct{}{}
				<-release
			}
		}))

		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, id := range mirrorIDs {
			wg.Add(1)
			go func(resourceID uuid.UUID) {
				defer wg.Done()
				_, err := ctx.service.List(ctx.users[0], TargetRef{Kind: TargetKindFeedArticle, ResourceID: resourceID}, ListCommentsInput{Page: 1})
				errs <- err
			}(id)
		}
		<-arrived
		<-arrived
		close(release)
		wg.Wait()
		close(errs)
		require.NoError(t, ctx.db.Callback().Query().Remove(callbackName))
		for err := range errs {
			require.NoError(t, err)
		}
	}
}

func TestCanonicalListRefreshCanInterleaveWithMutation(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindFeedArticle, 0)
	primaryID := ctx.target.ResourceID
	alternateID := uuid.New()
	canonicalKey := ctx.resolved.ResourceKey
	oldResolved := make(chan struct{}, 1)
	continueMutation := make(chan struct{})
	blockPrimary := false
	ctx.service.registry.resolvers[TargetKindFeedArticle] = targetResolverFunc(func(_ Viewer, id uuid.UUID) (ResolvedTarget, error) {
		if id != primaryID && id != alternateID {
			return ResolvedTarget{}, ErrTargetNotFound
		}
		if id == primaryID && blockPrimary {
			oldResolved <- struct{}{}
			<-continueMutation
		}
		return ResolvedTarget{Kind: TargetKindFeedArticle, ResourceID: id, ResourceKey: canonicalKey, Visible: true}, nil
	})
	created := ctx.create(t, 0, "shared", nil)
	blockPrimary = true
	mutationErr := make(chan error, 1)
	go func() { mutationErr <- ctx.service.Like(ctx.users[1], created.ID) }()
	<-oldResolved
	_, err := ctx.service.List(ctx.users[0], TargetRef{Kind: TargetKindFeedArticle, ResourceID: alternateID}, ListCommentsInput{Page: 1})
	require.NoError(t, err)
	close(continueMutation)
	require.NoError(t, <-mutationErr)
}

func TestDeleteUsesFreshResolvedOwnerInsteadOfCachedOwner(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	created := ctx.create(t, 2, "owned target", nil)
	resolved := ctx.resolved
	resolved.OwnerID = &ctx.users[1].ID
	ctx.service.registry.resolvers[TargetKindBlogPost] = targetResolverFunc(func(_ Viewer, id uuid.UUID) (ResolvedTarget, error) {
		resolved.ResourceID = id
		return resolved, nil
	})
	require.ErrorIs(t, ctx.service.Delete(ctx.users[0], created.ID), ErrCommentForbidden)
	require.NoError(t, ctx.service.Delete(ctx.users[1], created.ID))
}

func TestVisibleFoldedInteractionsAllowedAndModeratorHiddenRejected(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	folded := ctx.create(t, 0, "folded", nil)
	hidden := ctx.create(t, 0, "hidden", nil)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", folded.ID).Update("status", "auto_folded").Error)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", hidden.ID).Update("status", "moderator_hidden").Error)
	edited, err := ctx.service.Edit(ctx.users[0], folded.ID, EditCommentInput{Content: "edited folded"})
	require.NoError(t, err)
	require.Equal(t, "edited folded", edited.Content)
	require.NoError(t, ctx.service.Like(ctx.users[1], folded.ID))
	require.NoError(t, ctx.service.Unlike(ctx.users[1], folded.ID))
	_, err = ctx.service.Edit(ctx.users[0], hidden.ID, EditCommentInput{Content: "blocked"})
	require.ErrorIs(t, err, ErrCommentNotFound)
	require.ErrorIs(t, ctx.service.Like(ctx.users[1], hidden.ID), ErrCommentNotFound)
}

func TestUnmarkWithoutCommentsDoesNotCreateDiscussionTarget(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	require.NoError(t, ctx.service.Unmark(ctx.users[0], ctx.target))
	var count int64
	require.NoError(t, ctx.db.Model(&model.DiscussionTarget{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestCommentHierarchyLocksTargetThenRootThenChild(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "root", nil)
	child := ctx.create(t, 1, "child", &root.ID)
	var located model.CommentEntry
	require.NoError(t, ctx.db.First(&located, "id = ?", child.ID).Error)

	lockedTables := make([]string, 0, 3)
	callbackName := "comment-lock-order-" + uuid.NewString()
	require.NoError(t, ctx.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if _, locked := tx.Statement.Clauses["FOR"]; locked {
			lockedTables = append(lockedTables, tx.Statement.Table)
		}
	}))
	t.Cleanup(func() { _ = ctx.db.Callback().Query().Remove(callbackName) })

	require.NoError(t, ctx.db.Transaction(func(tx *gorm.DB) error {
		_, err := ctx.service.repo.lockCommentHierarchy(tx, located)
		return err
	}))
	require.Equal(t, []string{"discussion_targets", "comment_entries", "comment_entries"}, lockedTables)

	lockedTables = lockedTables[:0]
	require.NoError(t, ctx.db.Transaction(func(tx *gorm.DB) error {
		if _, err := ctx.service.repo.lockTargetByID(tx, located.TargetID); err != nil {
			return err
		}
		_, _, err := ctx.service.repo.lockReplyHierarchy(tx, located.TargetID, located.ID)
		return err
	}))
	require.Equal(t, []string{"discussion_targets", "comment_entries", "comment_entries"}, lockedTables)
}

func TestDeleteAllowsVisibleStatusesAndRejectsModeratorHidden(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "root", nil)
	folded := ctx.create(t, 1, "folded", &root.ID)
	hidden := ctx.create(t, 2, "hidden", &root.ID)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", folded.ID).Update("status", "auto_folded").Error)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", hidden.ID).Update("status", "moderator_hidden").Error)
	require.NoError(t, ctx.db.Model(&model.DiscussionTarget{}).Where("id = (SELECT target_id FROM comment_entries WHERE id = ?)", root.ID).Update("comment_count", 2).Error)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", root.ID).Update("reply_count", 1).Error)

	require.ErrorIs(t, ctx.service.Delete(ctx.users[2], hidden.ID), ErrCommentNotFound)
	require.NoError(t, ctx.service.Delete(ctx.users[1], folded.ID))
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.First(&target).Error)
	require.Equal(t, 1, target.CommentCount)
	var storedRoot model.CommentEntry
	require.NoError(t, ctx.db.First(&storedRoot, "id = ?", root.ID).Error)
	require.Zero(t, storedRoot.ReplyCount)
	var stillHidden model.CommentEntry
	require.NoError(t, ctx.db.First(&stillHidden, "id = ?", hidden.ID).Error)
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

func TestDeletePermissionsAllowAdminAndRequireActiveUser(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 1, "root", nil)
	admin := ctx.users[2]
	admin.Role = authctx.RoleAdmin
	require.NoError(t, ctx.service.Delete(admin, root.ID))
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
	alternateID := uuid.New()
	resolved := ctx.resolved
	ctx.service.registry.resolvers[TargetKindForumTopic] = targetResolverFunc(func(_ Viewer, id uuid.UUID) (ResolvedTarget, error) {
		resolved.ResourceID = id
		return resolved, nil
	})
	require.NoError(t, ctx.service.Mark(ctx.users[0], TargetRef{Kind: TargetKindForumTopic, ResourceID: alternateID}, second.ID))
	var refreshedTarget model.DiscussionTarget
	require.NoError(t, ctx.db.First(&refreshedTarget).Error)
	require.Equal(t, alternateID, refreshedTarget.ResourceID)
	listed, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1})
	require.NoError(t, err)
	require.Equal(t, second.ID, listed.Items[0].ID)
	require.True(t, listed.Items[0].Marked)

	other := newCommentTestContext(t, TargetKindBlogPost, 0)
	foreign := other.create(t, 0, "foreign", nil)
	require.ErrorIs(t, ctx.service.Mark(ctx.users[0], ctx.target, foreign.ID), ErrInvalidMark)
	require.NoError(t, ctx.service.Unmark(ctx.users[0], ctx.target))

	ownerless := resolved
	ownerless.OwnerID = nil
	ctx.service.registry.resolvers[TargetKindForumTopic] = targetResolverFunc(func(Viewer, uuid.UUID) (ResolvedTarget, error) { return ownerless, nil })
	require.ErrorIs(t, ctx.service.Mark(ctx.users[0], ctx.target, first.ID), ErrCommentForbidden)
}

func TestInteractionErrorsAreComparable(t *testing.T) {
	require.True(t, errors.Is(ErrCommentForbidden, ErrCommentForbidden))
	require.True(t, errors.Is(ErrCommentNotFound, ErrCommentNotFound))
	require.True(t, errors.Is(ErrInvalidMark, ErrInvalidMark))
}

func TestEditWithExtensionRollsBackCommentRelations(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	created, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "before"})
	require.NoError(t, err)
	want := errors.New("typed edit failed")
	_, err = ctx.service.EditWithExtension(ctx.users[0], created.ID, EditCommentInput{Content: "after"}, func(*gorm.DB, *model.CommentEntry) error { return want })
	require.ErrorIs(t, err, want)
	var stored model.CommentEntry
	require.NoError(t, ctx.db.First(&stored, "id = ?", created.ID).Error)
	require.Equal(t, "before", stored.Content)
}

func TestDeleteWithExtensionRollsBackCommentDelete(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	created, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "keep"})
	require.NoError(t, err)
	want := errors.New("typed delete failed")
	err = ctx.service.DeleteWithExtension(ctx.users[0], created.ID, func(*gorm.DB, []uuid.UUID) error { return want })
	require.ErrorIs(t, err, want)
	var count int64
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", created.ID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}
