package comment

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func addCommentUser(t *testing.T, ctx commentTestContext, role string) authctx.CurrentUser {
	t.Helper()
	i := uuid.NewString()
	stored := model.User{Username: "u-" + i, Email: fmt.Sprintf("%s@example.com", i), Password: "hash", IsActive: true, Role: role}
	require.NoError(t, ctx.db.Create(&stored).Error)
	return authctx.CurrentUser{ID: stored.UUID, Username: stored.Username, Role: role}
}

func TestFourthDistinctReportAutoFoldsAndDuplicateIsIdempotent(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	comment := ctx.create(t, 0, "report me", nil)
	for i := 0; i < 4; i++ {
		reporter := addCommentUser(t, ctx, authctx.RoleUser)
		require.NoError(t, ctx.service.Report(reporter, comment.ID, ReportInput{Reason: ReportReasonSpam}))
		require.NoError(t, ctx.service.Report(reporter, comment.ID, ReportInput{Reason: ReportReasonSpam}))
		var entry model.CommentEntry
		require.NoError(t, ctx.db.First(&entry, "id = ?", comment.ID).Error)
		require.Equal(t, i+1, entry.ReportCount)
		if i < 3 {
			require.Equal(t, CommentStatusActive, entry.Status)
		} else {
			require.Equal(t, CommentStatusAutoFolded, entry.Status)
		}
	}
	assertTargetCounters(t, ctx, 1, 1)
}

func TestReportValidationAndLifetimeUniqueness(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	comment := ctx.create(t, 0, "report me", nil)
	require.ErrorIs(t, ctx.service.Report(ctx.users[0], comment.ID, ReportInput{Reason: ReportReasonSpam}), ErrCommentForbidden)
	require.ErrorIs(t, ctx.service.Report(ctx.users[1], comment.ID, ReportInput{Reason: "bad"}), ErrInvalidReport)
	require.ErrorIs(t, ctx.service.Report(ctx.users[1], comment.ID, ReportInput{Reason: ReportReasonOther, Note: "  "}), ErrInvalidReport)
	require.NoError(t, ctx.service.Report(ctx.users[1], comment.ID, ReportInput{Reason: ReportReasonOther, Note: " context "}))

	moderator := addCommentUser(t, ctx, authctx.RoleModerator)
	var report model.CommentReport
	require.NoError(t, ctx.db.First(&report, "comment_id = ?", comment.ID).Error)
	require.NoError(t, ctx.service.Moderate(moderator, comment.ID, ModerateInput{Action: ModerationRejectReport, ReportID: &report.ID, Reason: "not abuse"}))
	require.NoError(t, ctx.service.Report(ctx.users[1], comment.ID, ReportInput{Reason: ReportReasonSpam}))
	var count int64
	require.NoError(t, ctx.db.Model(&model.CommentReport{}).Where("comment_id = ?", comment.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestReportRejectsHiddenDeletedAndMissingComments(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	hidden := ctx.create(t, 0, "hidden", nil)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", hidden.ID).Update("status", CommentStatusModeratorHidden).Error)
	require.ErrorIs(t, ctx.service.Report(ctx.users[1], hidden.ID, ReportInput{Reason: ReportReasonSpam}), ErrCommentNotFound)
	deleted := ctx.create(t, 0, "deleted", nil)
	require.NoError(t, ctx.service.Delete(ctx.users[0], deleted.ID))
	require.ErrorIs(t, ctx.service.Report(ctx.users[1], deleted.ID, ReportInput{Reason: ReportReasonSpam}), ErrCommentNotFound)
	require.ErrorIs(t, ctx.service.Report(ctx.users[1], uuid.New(), ReportInput{Reason: ReportReasonSpam}), ErrCommentNotFound)
}

func TestModerateReportRestoresFoldedAndWritesAudit(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	comment := ctx.create(t, 0, "reported", nil)
	var reports []model.CommentReport
	for i := 0; i < 4; i++ {
		reporter := addCommentUser(t, ctx, authctx.RoleUser)
		require.NoError(t, ctx.service.Report(reporter, comment.ID, ReportInput{Reason: ReportReasonSpam}))
	}
	require.NoError(t, ctx.db.Where("comment_id = ?", comment.ID).Order("id").Find(&reports).Error)
	require.ErrorIs(t, ctx.service.Moderate(ctx.users[1], comment.ID, ModerateInput{Action: ModerationRejectReport, ReportID: &reports[0].ID}), ErrCommentForbidden)
	moderator := addCommentUser(t, ctx, authctx.RoleModerator)
	require.NoError(t, ctx.service.Moderate(moderator, comment.ID, ModerateInput{Action: ModerationRejectReport, ReportID: &reports[0].ID, Reason: "reviewed"}))

	var entry model.CommentEntry
	require.NoError(t, ctx.db.First(&entry, "id = ?", comment.ID).Error)
	require.Equal(t, 3, entry.ReportCount)
	require.Equal(t, CommentStatusActive, entry.Status)
	var audit model.AuditLog
	require.NoError(t, ctx.db.Where("entity_id = ?", comment.ID).Last(&audit).Error)
	require.Equal(t, "comment.moderate.reject_report", audit.Action)
	require.Equal(t, moderator.ID, *audit.ActorID)
	require.Equal(t, "reviewed", audit.Reason)
	require.NoError(t, ctx.db.First(&reports[0], "id = ?", reports[0].ID).Error)
	require.Equal(t, "rejected", reports[0].Status)
	require.Equal(t, moderator.ID, *reports[0].ReviewerID)
	require.NotNil(t, reports[0].ReviewedAt)
}

func TestModerationRestoreExplicitlyUnfoldsWithoutChangingReportsOrCounters(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	comment := ctx.create(t, 0, "reported", nil)
	for i := 0; i < 4; i++ {
		reporter := addCommentUser(t, ctx, authctx.RoleUser)
		require.NoError(t, ctx.service.Report(reporter, comment.ID, ReportInput{Reason: ReportReasonSpam}))
	}
	moderator := addCommentUser(t, ctx, authctx.RoleModerator)
	require.NoError(t, ctx.service.Moderate(moderator, comment.ID, ModerateInput{Action: ModerationRestore}))
	var entry model.CommentEntry
	require.NoError(t, ctx.db.First(&entry, "id = ?", comment.ID).Error)
	require.Equal(t, CommentStatusActive, entry.Status)
	require.Equal(t, 4, entry.ReportCount)
	var reportCount int64
	require.NoError(t, ctx.db.Model(&model.CommentReport{}).Where("comment_id = ?", comment.ID).Count(&reportCount).Error)
	require.EqualValues(t, 4, reportCount)
	assertTargetCounters(t, ctx, 1, 1)
}

func TestModerateActionsAndAuditFailureRollback(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	comment := ctx.create(t, 0, "moderate", nil)
	moderator := addCommentUser(t, ctx, authctx.RoleModerator)
	require.ErrorIs(t, ctx.service.Moderate(moderator, comment.ID, ModerateInput{Action: "unknown"}), ErrInvalidModeration)
	require.NoError(t, ctx.service.Moderate(moderator, comment.ID, ModerateInput{Action: ModerationHide, Reason: "hide"}))
	var entry model.CommentEntry
	require.NoError(t, ctx.db.First(&entry, "id = ?", comment.ID).Error)
	require.Equal(t, CommentStatusModeratorHidden, entry.Status)
	require.NoError(t, ctx.service.Moderate(moderator, comment.ID, ModerateInput{Action: ModerationRestore, Reason: "restore"}))
	require.NoError(t, ctx.db.First(&entry, "id = ?", comment.ID).Error)
	require.Equal(t, CommentStatusActive, entry.Status)

	callback := "test:reject-comment-audit"
	require.NoError(t, ctx.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			tx.AddError(errors.New("audit unavailable"))
		}
	}))
	t.Cleanup(func() { _ = ctx.db.Callback().Create().Remove(callback) })
	require.Error(t, ctx.service.Moderate(moderator, comment.ID, ModerateInput{Action: ModerationHide}))
	require.NoError(t, ctx.db.First(&entry, "id = ?", comment.ID).Error)
	require.Equal(t, CommentStatusActive, entry.Status)
	require.NoError(t, ctx.db.Callback().Create().Remove(callback))

	child := ctx.create(t, 1, "child", &comment.ID)
	require.NoError(t, ctx.service.Moderate(moderator, child.ID, ModerateInput{Action: ModerationDelete, Reason: "delete"}))
	require.ErrorIs(t, ctx.service.Report(ctx.users[2], child.ID, ReportInput{Reason: ReportReasonSpam}), ErrCommentNotFound)
}

func TestModerateHideRestoreRootUsesEffectiveVisibleCounters(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 1, "root", nil)
	active := ctx.create(t, 2, "active", &root.ID)
	folded := ctx.create(t, 2, "folded", &root.ID)
	hidden := ctx.create(t, 2, "hidden", &root.ID)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", folded.ID).Update("status", CommentStatusAutoFolded).Error)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", hidden.ID).Update("status", CommentStatusModeratorHidden).Error)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", root.ID).Update("status", CommentStatusAutoFolded).Error)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", root.ID).Update("reply_count", 2).Error)
	require.NoError(t, ctx.db.Model(&model.DiscussionTarget{}).Where("kind = ?", TargetKindBlogPost).Updates(map[string]any{"comment_count": 3, "pinned_comment_id": root.ID}).Error)
	moderator := addCommentUser(t, ctx, authctx.RoleModerator)

	require.NoError(t, ctx.service.Moderate(moderator, root.ID, ModerateInput{Action: ModerationHide}))
	require.NoError(t, ctx.service.Moderate(moderator, root.ID, ModerateInput{Action: ModerationHide}))
	assertTargetCounters(t, ctx, 0, 0)
	list, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1})
	require.NoError(t, err)
	require.Zero(t, list.TotalComments)
	require.Zero(t, list.TotalReplies)
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.Where("kind = ?", TargetKindBlogPost).First(&target).Error)
	require.Nil(t, target.PinnedCommentID)

	require.NoError(t, ctx.service.Moderate(moderator, root.ID, ModerateInput{Action: ModerationRestore}))
	require.NoError(t, ctx.service.Moderate(moderator, root.ID, ModerateInput{Action: ModerationRestore}))
	assertTargetCounters(t, ctx, 3, 1)
	var restored, stillHidden model.CommentEntry
	require.NoError(t, ctx.db.First(&restored, "id = ?", root.ID).Error)
	require.Equal(t, 2, restored.ReplyCount)
	require.NoError(t, ctx.db.First(&stillHidden, "id = ?", hidden.ID).Error)
	require.Equal(t, CommentStatusModeratorHidden, stillHidden.Status)
	var activeAfter, foldedAfter model.CommentEntry
	require.NoError(t, ctx.db.First(&activeAfter, "id = ?", active.ID).Error)
	require.Equal(t, CommentStatusActive, activeAfter.Status)
	require.NoError(t, ctx.db.First(&foldedAfter, "id = ?", folded.ID).Error)
	require.Equal(t, CommentStatusAutoFolded, foldedAfter.Status)
	var auditCount int64
	require.NoError(t, ctx.db.Model(&model.AuditLog{}).Where("entity_id = ?", root.ID).Count(&auditCount).Error)
	require.EqualValues(t, 4, auditCount)
}

func TestModerateHideRestoreChildKeepsCountersRepliesAndHotScoreAccurate(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "root", nil)
	child := ctx.create(t, 1, "child", &root.ID)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", child.ID).Update("status", CommentStatusAutoFolded).Error)
	moderator := addCommentUser(t, ctx, authctx.RoleModerator)
	var before model.CommentEntry
	require.NoError(t, ctx.db.First(&before, "id = ?", root.ID).Error)
	require.NoError(t, ctx.service.Moderate(moderator, child.ID, ModerateInput{Action: ModerationHide}))
	require.NoError(t, ctx.service.Moderate(moderator, child.ID, ModerateInput{Action: ModerationHide}))
	assertTargetCounters(t, ctx, 1, 1)
	var hiddenRoot model.CommentEntry
	require.NoError(t, ctx.db.First(&hiddenRoot, "id = ?", root.ID).Error)
	require.Zero(t, hiddenRoot.ReplyCount)
	require.NotEqual(t, before.HotScore, hiddenRoot.HotScore)
	require.NoError(t, ctx.service.Moderate(moderator, child.ID, ModerateInput{Action: ModerationRestore}))
	require.NoError(t, ctx.service.Moderate(moderator, child.ID, ModerateInput{Action: ModerationRestore}))
	assertTargetCounters(t, ctx, 2, 1)
	var restoredRoot model.CommentEntry
	require.NoError(t, ctx.db.First(&restoredRoot, "id = ?", root.ID).Error)
	require.Equal(t, 1, restoredRoot.ReplyCount)
}

func TestModerateDeleteHiddenCommentsDoesNotDoubleDecrement(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	moderator := addCommentUser(t, ctx, authctx.RoleModerator)
	root := ctx.create(t, 0, "root", nil)
	child := ctx.create(t, 1, "child", &root.ID)
	require.NoError(t, ctx.service.Moderate(moderator, root.ID, ModerateInput{Action: ModerationHide}))
	require.NoError(t, ctx.service.Moderate(moderator, root.ID, ModerateInput{Action: ModerationDelete}))
	assertTargetCounters(t, ctx, 0, 0)
	var count int64
	require.NoError(t, ctx.db.Unscoped().Model(&model.CommentEntry{}).Where("id IN ?", []uuid.UUID{root.ID, child.ID}).Count(&count).Error)
	require.Zero(t, count)

	root = ctx.create(t, 0, "root-2", nil)
	child = ctx.create(t, 1, "child-2", &root.ID)
	require.NoError(t, ctx.service.Moderate(moderator, child.ID, ModerateInput{Action: ModerationHide}))
	require.NoError(t, ctx.service.Moderate(moderator, child.ID, ModerateInput{Action: ModerationDelete}))
	assertTargetCounters(t, ctx, 1, 1)
}

func TestHiddenRootBlocksOrdinaryChildMutations(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "root", nil)
	child := ctx.create(t, 1, "child", &root.ID)
	moderator := addCommentUser(t, ctx, authctx.RoleModerator)
	require.NoError(t, ctx.service.Moderate(moderator, root.ID, ModerateInput{Action: ModerationHide}))
	_, err := ctx.service.Edit(ctx.users[1], child.ID, EditCommentInput{Content: "edited"})
	require.ErrorIs(t, err, ErrCommentNotFound)
	require.ErrorIs(t, ctx.service.Delete(ctx.users[1], child.ID), ErrCommentNotFound)
	require.ErrorIs(t, ctx.service.Like(ctx.users[2], child.ID), ErrCommentNotFound)
	require.ErrorIs(t, ctx.service.Report(ctx.users[2], child.ID, ReportInput{Reason: ReportReasonSpam}), ErrCommentNotFound)
}

func assertTargetCounters(t *testing.T, ctx commentTestContext, comments, roots int) {
	t.Helper()
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.Where("kind = ?", TargetKindBlogPost).First(&target).Error)
	require.Equal(t, comments, target.CommentCount)
	require.Equal(t, roots, target.RootCount)
}

func TestNotificationReplyMentionEditMarkAndDelete(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 1, "root", nil)
	content := "hi @" + ctx.users[1].Username + " @" + ctx.users[2].Username
	firstStart := len([]rune("hi "))
	secondStart := firstStart + len([]rune("@"+ctx.users[1].Username+" "))
	child, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: content, ReplyToID: &root.ID, Mentions: []MentionInput{{UserID: ctx.users[1].ID, Start: firstStart, End: firstStart + len([]rune("@"+ctx.users[1].Username))}, {UserID: ctx.users[2].ID, Start: secondStart, End: secondStart + len([]rune("@"+ctx.users[2].Username))}}})
	require.NoError(t, err)
	var notifications []model.Notification
	require.NoError(t, ctx.db.Where("source_id = ?", child.ID).Order("recipient_id").Find(&notifications).Error)
	require.Len(t, notifications, 2)
	for _, notification := range notifications {
		require.Equal(t, ctx.target.ResourceID.String(), notification.Meta["resource_id"])
		require.Equal(t, child.ID.String(), notification.Meta["comment_id"])
		require.Equal(t, root.ID.String(), notification.Meta["root_id"])
	}

	editContent := content + " @" + ctx.users[3].Username
	thirdStart := len([]rune(content + " "))
	_, err = ctx.service.Edit(ctx.users[0], child.ID, EditCommentInput{Content: editContent, Mentions: []MentionInput{{UserID: ctx.users[1].ID, Start: firstStart, End: firstStart + len([]rune("@"+ctx.users[1].Username))}, {UserID: ctx.users[2].ID, Start: secondStart, End: secondStart + len([]rune("@"+ctx.users[2].Username))}, {UserID: ctx.users[3].ID, Start: thirdStart, End: thirdStart + len([]rune("@"+ctx.users[3].Username))}}})
	require.NoError(t, err)
	require.NoError(t, ctx.db.Where("source_id = ?", child.ID).Find(&notifications).Error)
	require.Len(t, notifications, 3)

	require.NoError(t, ctx.service.Mark(ctx.users[0], ctx.target, root.ID))
	require.NoError(t, ctx.service.Mark(ctx.users[0], ctx.target, root.ID))
	require.NoError(t, ctx.service.Delete(ctx.users[1], root.ID))
	require.NoError(t, ctx.db.Unscoped().Where("source_id IN ? AND source_type LIKE ?", []uuid.UUID{root.ID, child.ID}, "comment_%").Find(&notifications).Error)
	require.Empty(t, notifications)
}

func TestConcurrentRateLimitAcrossTargetsAllowsOnlyOneOfFifthAndSixth(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	secondID := uuid.New()
	ctx.service.registry.resolvers[TargetKindBlogPost] = targetResolverFunc(func(_ Viewer, id uuid.UUID) (ResolvedTarget, error) {
		if id != ctx.target.ResourceID && id != secondID {
			return ResolvedTarget{}, ErrTargetNotFound
		}
		return ResolvedTarget{Kind: TargetKindBlogPost, ResourceID: id, ResourceKey: id.String(), OwnerID: &ctx.users[0].ID, Visible: true}, nil
	})
	ctx.service.checkAbuse = true
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	ctx.service.now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		_, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: fmt.Sprintf("seed-%d", i)})
		require.NoError(t, err)
	}

	targets := []TargetRef{ctx.target, {Kind: TargetKindBlogPost, ResourceID: secondID}}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target TargetRef) {
			defer wg.Done()
			_, err := ctx.service.Create(ctx.users[0], target, CreateCommentInput{Content: fmt.Sprintf("concurrent-%d", i)})
			errs <- err
		}(i, target)
	}
	wg.Wait()
	close(errs)
	successes, limited := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrCommentRateLimited) {
			limited++
		} else {
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, limited)
}

func TestLikeAggregateCountsOnlyExternalLikesInCurrentUnreadWindow(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	comment := ctx.create(t, 0, "like", nil)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	ctx.service.now = func() time.Time { return now }
	windowStart := now
	require.NoError(t, ctx.service.Like(ctx.users[0], comment.ID))
	var notifications []model.Notification
	require.NoError(t, ctx.db.Where("recipient_id = ? AND aggregation_key <> '' AND read_at IS NULL", ctx.users[0].ID).Find(&notifications).Error)
	require.Empty(t, notifications)
	require.NoError(t, ctx.service.Like(ctx.users[1], comment.ID))
	require.NoError(t, ctx.service.Like(ctx.users[1], comment.ID))
	now = now.Add(time.Second)
	require.NoError(t, ctx.service.Like(ctx.users[2], comment.ID))
	require.NoError(t, ctx.db.Where("recipient_id = ? AND aggregation_key <> '' AND read_at IS NULL", ctx.users[0].ID).Find(&notifications).Error)
	require.Len(t, notifications, 1)
	require.EqualValues(t, 2, notifications[0].Meta["like_count"])
	require.True(t, notifications[0].CreatedAt.Equal(windowStart))
	require.Equal(t, ctx.users[2].ID, *notifications[0].ActorID)
	require.NoError(t, ctx.service.Unlike(ctx.users[0], comment.ID))
	require.NoError(t, ctx.service.Unlike(ctx.users[1], comment.ID))
	require.NoError(t, ctx.db.Where("recipient_id = ? AND aggregation_key <> '' AND read_at IS NULL", ctx.users[0].ID).Find(&notifications).Error)
	require.Len(t, notifications, 1)
	require.EqualValues(t, 1, notifications[0].Meta["like_count"])
	require.Equal(t, ctx.users[2].ID, *notifications[0].ActorID)
	require.NoError(t, ctx.service.Unlike(ctx.users[2], comment.ID))
	require.NoError(t, ctx.db.Where("recipient_id = ? AND aggregation_key <> '' AND read_at IS NULL", ctx.users[0].ID).Find(&notifications).Error)
	require.Empty(t, notifications)
}

func TestLikeAggregateStartsFreshAfterPreviousAggregateWasRead(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	comment := ctx.create(t, 0, "like", nil)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	ctx.service.now = func() time.Time { return now }
	require.NoError(t, ctx.service.Like(ctx.users[1], comment.ID))
	var first model.Notification
	require.NoError(t, ctx.db.Where("recipient_id = ? AND read_at IS NULL AND aggregation_key <> ''", ctx.users[0].ID).First(&first).Error)
	readAt := now.Add(time.Second)
	require.NoError(t, ctx.db.Model(&first).Update("read_at", readAt).Error)
	now = now.Add(time.Minute)
	require.NoError(t, ctx.service.Like(ctx.users[2], comment.ID))
	var unread model.Notification
	require.NoError(t, ctx.db.Where("recipient_id = ? AND read_at IS NULL AND aggregation_key <> ''", ctx.users[0].ID).First(&unread).Error)
	require.EqualValues(t, 1, unread.Meta["like_count"])
	require.Equal(t, ctx.users[2].ID, *unread.ActorID)
	require.NoError(t, ctx.service.Unlike(ctx.users[1], comment.ID))
	require.NoError(t, ctx.db.First(&unread, "id = ?", unread.ID).Error)
	require.EqualValues(t, 1, unread.Meta["like_count"])
	require.Equal(t, ctx.users[2].ID, *unread.ActorID)
	require.NoError(t, ctx.service.Unlike(ctx.users[2], comment.ID))
	var count int64
	require.NoError(t, ctx.db.Model(&model.Notification{}).Where("id = ?", unread.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestRateLimitAndDuplicateCommentUseInjectedClock(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	ctx.service.now = func() time.Time { return now }
	ctx.service.checkAbuse = true
	for i := 0; i < 5; i++ {
		_, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: fmt.Sprintf("comment-%d", i)})
		require.NoError(t, err)
	}
	var ledgerCount int64
	require.NoError(t, ctx.db.Model(&model.CommentPublishRecord{}).Where("author_id = ?", ctx.users[0].ID).Count(&ledgerCount).Error)
	require.EqualValues(t, 5, ledgerCount)
	writerCalled := false
	_, err := ctx.service.CreateWithExtension(ctx.users[0], ctx.target, CreateCommentInput{Content: "sixth"}, func(_ *gorm.DB, _ *model.CommentEntry) error {
		writerCalled = true
		return nil
	})
	require.ErrorIs(t, err, ErrCommentRateLimited)
	require.False(t, writerCalled)
	now = now.Add(time.Minute)
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "sixth"})
	require.NoError(t, err)
	_, err = ctx.service.Create(ctx.users[1], ctx.target, CreateCommentInput{Content: "duplicate"})
	require.NoError(t, err)
	_, err = ctx.service.Create(ctx.users[1], ctx.target, CreateCommentInput{Content: "duplicate"})
	require.ErrorIs(t, err, ErrDuplicateComment)
	now = now.Add(5 * time.Minute)
	_, err = ctx.service.Create(ctx.users[1], ctx.target, CreateCommentInput{Content: "duplicate"})
	require.NoError(t, err)
	require.True(t, errors.Is(ErrCommentRateLimited, ErrCommentRateLimited))
}

func TestPublishLedgerPreventsPhysicalDeleteFromBypassingAbuseWindows(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	ctx.service.now = func() time.Time { return now }
	ctx.service.checkAbuse = true
	for i := 0; i < 5; i++ {
		comment, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: fmt.Sprintf("deleted-%d", i)})
		require.NoError(t, err)
		require.NoError(t, ctx.service.Delete(ctx.users[0], comment.ID))
	}
	_, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "sixth"})
	require.ErrorIs(t, err, ErrCommentRateLimited)

	comment, err := ctx.service.Create(ctx.users[1], ctx.target, CreateCommentInput{Content: "same"})
	require.NoError(t, err)
	require.NoError(t, ctx.service.Delete(ctx.users[1], comment.ID))
	_, err = ctx.service.Create(ctx.users[1], ctx.target, CreateCommentInput{Content: "same"})
	require.ErrorIs(t, err, ErrDuplicateComment)
}

func TestFailedExtensionDoesNotWritePublishRecord(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	ctx.service.checkAbuse = true
	expected := errors.New("extension failed")
	_, err := ctx.service.CreateWithExtension(ctx.users[0], ctx.target, CreateCommentInput{Content: "fails"}, func(_ *gorm.DB, _ *model.CommentEntry) error { return expected })
	require.ErrorIs(t, err, expected)
	var count int64
	require.NoError(t, ctx.db.Model(&model.CommentPublishRecord{}).Where("author_id = ?", ctx.users[0].ID).Count(&count).Error)
	require.Zero(t, count)
}
