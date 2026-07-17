package comment

import (
	"errors"
	"testing"

	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type forumPolicyStub struct {
	viewErr       error
	createErr     error
	updateErr     error
	viewers       []Viewer
	createCalls   int
	updateCalls   int
	evaluateCalls int
	onUpdate      func()
	audienceTitle string
	audienceIDs   []uuid.UUID
	audienceErr   error
}

func (p *forumPolicyStub) CanViewTopic(viewer Viewer, _ uuid.UUID) error {
	p.viewers = append(p.viewers, viewer)
	return p.viewErr
}

func (p *forumPolicyStub) CheckCreateComment(_ *gorm.DB, _ authctx.CurrentUser, _ uuid.UUID, _ string) error {
	p.createCalls++
	return p.createErr
}

func (p *forumPolicyStub) CheckUpdateComment(_ *gorm.DB, _ uuid.UUID, _ uuid.UUID, _ string) error {
	p.updateCalls++
	if p.onUpdate != nil {
		p.onUpdate()
	}
	return p.updateErr
}

func (p *forumPolicyStub) EvaluateTrust(_ uuid.UUID) {
	p.evaluateCalls++
}

func (p *forumPolicyStub) CommentNotificationAudience(_ *gorm.DB, _ uuid.UUID, _ uuid.UUID) (string, []uuid.UUID, error) {
	return p.audienceTitle, p.audienceIDs, p.audienceErr
}

func TestForumPolicyProtectsListGetRepliesAndLikeAndCarriesRole(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindForumTopic, 0)
	root := ctx.create(t, 0, "root", nil)
	child := ctx.create(t, 1, "child", &root.ID)
	denied := errors.New("forum category hidden")
	policy := &forumPolicyStub{viewErr: denied}
	ctx.service.SetForumPolicy(policy)

	_, err := ctx.service.List(authctx.CurrentUser{Role: authctx.RoleAnonymous}, ctx.target, ListCommentsInput{Page: 1})
	require.ErrorIs(t, err, denied)
	_, err = ctx.service.Get(Viewer{UserID: &ctx.users[2].ID, Role: authctx.RoleUser}, child.ID)
	require.ErrorIs(t, err, denied)
	_, err = ctx.service.ListReplies(Viewer{UserID: &ctx.users[2].ID, Role: authctx.RoleUser}, root.ID, 1, 20)
	require.ErrorIs(t, err, denied)
	err = ctx.service.Like(authctx.CurrentUser{ID: ctx.users[3].ID, Role: authctx.RoleAdmin}, child.ID)
	require.ErrorIs(t, err, denied)

	require.Len(t, policy.viewers, 4)
	require.Equal(t, authctx.RoleAnonymous, policy.viewers[0].Role)
	require.Equal(t, authctx.RoleAdmin, policy.viewers[3].Role)
}

func TestForumPolicyProtectsDeleteReportUnlikeMarkAndUnmark(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindForumTopic, 0)
	root := ctx.create(t, 0, "root", nil)
	child := ctx.create(t, 1, "child", &root.ID)
	denied := errors.New("forum category hidden")
	policy := &forumPolicyStub{viewErr: denied}
	ctx.service.SetForumPolicy(policy)

	require.ErrorIs(t, ctx.service.Delete(ctx.users[1], child.ID), denied)
	require.ErrorIs(t, ctx.service.Report(ctx.users[2], child.ID, ReportInput{Reason: ReportReasonSpam}), denied)
	require.ErrorIs(t, ctx.service.Unlike(ctx.users[2], child.ID), denied)
	require.ErrorIs(t, ctx.service.Mark(ctx.users[0], ctx.target, root.ID), denied)
	require.ErrorIs(t, ctx.service.Unmark(ctx.users[0], ctx.target), denied)
	require.Len(t, policy.viewers, 5)
	for _, viewer := range policy.viewers {
		require.Equal(t, authctx.RoleUser, viewer.Role)
	}
}

func TestForumPolicyChecksCreateAndEvaluatesTrustAfterSuccess(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindForumTopic, 0)
	denied := errors.New("forum comment denied")
	policy := &forumPolicyStub{createErr: denied}
	ctx.service.SetForumPolicy(policy)

	_, err := ctx.service.Create(ctx.users[1], ctx.target, CreateCommentInput{Content: "blocked"})
	require.ErrorIs(t, err, denied)
	require.Equal(t, 1, policy.createCalls)
	require.Zero(t, policy.evaluateCalls)

	policy.createErr = nil
	_, err = ctx.service.Create(ctx.users[1], ctx.target, CreateCommentInput{Content: "allowed"})
	require.NoError(t, err)
	require.Equal(t, 2, policy.createCalls)
	require.Equal(t, 1, policy.evaluateCalls)
}

func TestForumPolicyChecksEditBeforeWriting(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindForumTopic, 0)
	created := ctx.create(t, 1, "original", nil)
	denied := errors.New("duplicate forum reply")
	policy := &forumPolicyStub{updateErr: denied}
	ctx.service.SetForumPolicy(policy)

	_, err := ctx.service.Edit(ctx.users[1], created.ID, EditCommentInput{Content: "duplicate"})
	require.ErrorIs(t, err, denied)
	require.Equal(t, 1, policy.updateCalls)

	stored, getErr := ctx.service.Get(Viewer{UserID: &ctx.users[1].ID, Role: authctx.RoleUser}, created.ID)
	require.NoError(t, getErr)
	require.Equal(t, "original", stored.Content)
}

func TestForumCommentEditLocksAuthorBeforeTargetAndComment(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindForumTopic, 0)
	created := ctx.create(t, 1, "original", nil)

	lockedTables := make([]string, 0, 3)
	callbackName := "forum-edit-lock-order-" + uuid.NewString()
	require.NoError(t, ctx.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if _, locked := tx.Statement.Clauses["FOR"]; locked {
			lockedTables = append(lockedTables, tx.Statement.Table)
		}
	}))
	t.Cleanup(func() { _ = ctx.db.Callback().Query().Remove(callbackName) })
	ctx.service.SetForumPolicy(&forumPolicyStub{onUpdate: func() {
		require.Equal(t, []string{"Users", "discussion_targets", "comment_entries"}, lockedTables)
	}})

	_, err := ctx.service.Edit(ctx.users[1], created.ID, EditCommentInput{Content: "updated"})
	require.NoError(t, err)
	require.Equal(t, []string{"Users", "discussion_targets", "comment_entries"}, lockedTables)
}
